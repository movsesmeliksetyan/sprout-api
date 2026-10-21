package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// MaxFileBytes is the largest statement read (PRD §4.5).
const MaxFileBytes = 15 << 20

// Processor runs the statement pipeline for imports (PRD §4.3).
type Processor struct {
	db    Database
	store storage.Store
	llm   llm.Client
	now   func() time.Time
}

// ProcessorOption configures a Processor.
type ProcessorOption func(*Processor)

// WithClock sets where the processor reads the time, which places an
// import's period window. It defaults to time.Now.
func WithClock(now func() time.Time) ProcessorOption {
	return func(p *Processor) { p.now = now }
}

// NewProcessor returns a processor reading files from store and asking
// client, which may be nil, about the files the deterministic stages
// cannot read.
func NewProcessor(database Database, store storage.Store, client llm.Client, opts ...ProcessorOption) *Processor {
	p := &Processor{db: database, store: store, llm: client, now: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// outcome is what the pipeline made of a file.
type outcome struct {
	status      Status
	format      statements.Format
	source      statements.MappingSource
	spec        *statements.MappingSpec
	request     *statements.MappingRequest
	fingerprint string
	bankHint    *string
	rows        []statements.Row
	outOfPeriod int
	failure     *Failure
}

// Process runs the pipeline for an import. It starts over every time —
// what an earlier attempt wrote is replaced — so a retried job rebuilds
// the same rows. A committed or discarded import is left alone. Problems
// with the file end the import as failed or needing a mapping; an error is
// returned only for what a retry may fix.
func (p *Processor) Process(ctx context.Context, importID uuid.UUID) error {
	q := db.New(p.db)
	source, err := q.GetImportSource(ctx, importID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // discarded
	}
	if err != nil {
		return fmt.Errorf("imports: process: %w", err)
	}
	if Status(source.Status) == StatusCommitted {
		return nil
	}
	if err := q.StartImportProcessing(ctx, importID); err != nil {
		return fmt.Errorf("imports: process: %w", err)
	}
	loc, err := time.LoadLocation(source.Timezone)
	if err != nil {
		loc = time.UTC
	}

	data, err := p.store.Get(ctx, source.ObjectKey, MaxFileBytes)
	var result outcome
	switch {
	case errors.Is(err, storage.ErrNotFound):
		result = failed(FailureEmptyFile, "The statement file is gone. Upload it again.")
	case errors.Is(err, storage.ErrTooLarge):
		result = failed(FailureTooManyRows, "The statement is larger than 15 MB.")
	case err != nil:
		return fmt.Errorf("imports: read statement: %w", err)
	default:
		hints := statements.Hints{}
		if source.Currency == "USD" {
			monthFirst := true
			hints.MonthFirst = &monthFirst
		}
		result, err = p.read(ctx, data, loc, hints)
		if err != nil {
			return err
		}
		if result.status == StatusReady {
			window, err := statements.NewWindow(source.Period, loc, p.now())
			if err != nil {
				return fmt.Errorf("imports: period: %w", err)
			}
			result = validate(result, window, loc)
		}
	}
	return p.save(ctx, importID, source, loc, result)
}

// read runs the stages that need no database writes: detect, extract,
// resolve and transform.
func (p *Processor) read(ctx context.Context, data []byte, loc *time.Location, hints statements.Hints) (outcome, error) {
	detected, err := statements.Detect(data)
	if err != nil {
		return fileFailure(err), nil
	}
	result := outcome{format: detected.Format}
	var table statements.RawTable
	switch detected.Format {
	case statements.FormatOFX:
		txs, err := statements.ExtractOFX(detected, loc)
		if err != nil {
			return withFormat(fileFailure(err), detected.Format), nil
		}
		result.status, result.source = StatusReady, statements.SourceNative
		for _, tx := range txs {
			result.rows = append(result.rows, statements.Row{NormalizedTransaction: tx})
		}
		return result, nil
	case statements.FormatCSV:
		table, err = statements.ExtractCSV(detected)
	case statements.FormatXLSX:
		table, err = statements.ExtractXLSX(data)
	case statements.FormatPDF:
		table, err = statements.ExtractPDF(ctx, data, p.llm)
	}
	if err != nil {
		return withFormat(fileFailure(err), detected.Format), nil
	}

	resolver := statements.NewResolver(db.New(p.db), statements.WithLLM(p.llm))
	resolution, err := resolver.Resolve(ctx, table, detected.Format, hints)
	result.fingerprint = resolution.Fingerprint
	switch {
	case errors.Is(err, statements.ErrNoMapping):
		request := statements.NewMappingRequest(table, hints)
		result.status, result.request = StatusNeedsMapping, &request
		return result, nil
	case err != nil:
		return outcome{}, fmt.Errorf("imports: mapping: %w", err)
	}
	result.status, result.source, result.spec, result.bankHint = StatusReady, resolution.Source, &resolution.Spec, resolution.BankHint
	result.rows = statements.Transform(table, resolution.Spec, loc)
	if resolution.Source == statements.SourceTemplate {
		succeeded := statements.ParseRate(result.rows) >= 95
		if err := resolver.RecordUse(ctx, resolution.Fingerprint, succeeded); err != nil {
			return outcome{}, err
		}
	}
	return result, nil
}

// validate checks the rows read and keeps those in the import's window.
func validate(result outcome, window statements.Window, loc *time.Location) outcome {
	validated, err := statements.Validate(result.rows, window, loc)
	if err != nil {
		return withFormat(fileFailure(err), result.format)
	}
	result.rows, result.outOfPeriod = validated.Rows, validated.OutOfPeriod
	return result
}

// save replaces the import's rows with the outcome's and records what was
// found, in one transaction.
func (p *Processor) save(ctx context.Context, importID uuid.UUID, source db.GetImportSourceRow, loc *time.Location, result outcome) error {
	return db.WithTx(ctx, p.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.DeleteImportRows(ctx, importID); err != nil {
			return fmt.Errorf("imports: process: %w", err)
		}
		if result.failure != nil {
			return fail(ctx, q, importID, *result.failure)
		}
		rows, err := draftRows(importID, source.UserID, loc, result.rows)
		if err != nil {
			return err
		}
		if err := markDuplicates(ctx, q, source.UserID, rows); err != nil {
			return err
		}
		summary := summarize(rows)
		summary.OutOfPeriodCount = result.outOfPeriod
		if len(rows) > 0 {
			if _, err := q.InsertImportRows(ctx, rows); err != nil {
				return fmt.Errorf("imports: rows: %w", err)
			}
		}
		params := db.FinishImportParams{ID: importID, Status: string(result.status), BankHint: result.bankHint}
		if result.format != "" {
			format := string(result.format)
			params.Format = &format
		}
		if result.source != "" {
			mappingSource := string(result.source)
			params.MappingSource = &mappingSource
		}
		if result.fingerprint != "" {
			params.HeaderFingerprint = &result.fingerprint
		}
		if params.MappingSpec, err = jsonOrNil(result.spec); err != nil {
			return err
		}
		if params.MappingRequest, err = jsonOrNil(result.request); err != nil {
			return err
		}
		if params.Summary, err = json.Marshal(summary); err != nil {
			return err
		}
		return q.FinishImport(ctx, params)
	})
}

// draftRows turns the pipeline's rows into import rows.
func draftRows(importID, userID uuid.UUID, loc *time.Location, rows []statements.Row) ([]db.InsertImportRowsParams, error) {
	out := make([]db.InsertImportRowsParams, 0, len(rows))
	for _, row := range rows {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("imports: new id: %w", err)
		}
		raw, err := json.Marshal(row.Raw)
		if err != nil {
			return nil, err
		}
		params := db.InsertImportRowsParams{
			ID: id, ImportID: importID, UserID: userID, RowIndex: int32(row.RowIndex), Raw: raw,
			RawDescription: row.RawDescription,
		}
		if row.Error != "" {
			params.Status, params.Included = "error", false
			params.Error = &row.Error
			out = append(out, params)
			continue
		}
		merchant := categorize.Normalize(row.RawDescription)
		kind := transactions.KindExpense
		if row.Direction == statements.Credit {
			kind = transactions.KindIncome
		}
		occurred := row.PostedAt
		localDate := period.LocalDate(occurred, loc)
		amount := row.AmountMinor
		kindName := string(kind)
		params.Status, params.Included, params.Kind = "ok", true, &kindName
		params.AmountMinor, params.OccurredAt, params.LocalDate = &amount, &occurred, &localDate
		params.MerchantKey = merchant.Key
		params.DedupHash = transactions.DedupHash(localDate, amount, kind, merchant.Key)
		if merchant.Display != "" {
			params.Merchant = &merchant.Display
		}
		if row.MCC != "" {
			mcc := row.MCC
			params.Mcc = &mcc
		}
		if row.ExternalRef != "" {
			ref := row.ExternalRef
			params.ExternalRef = &ref
		}
		none := string(categorize.SourceNone)
		params.CategorySource = &none
		out = append(out, params)
	}
	return out, nil
}

// markDuplicates marks the rows already in the user's ledger as
// duplicates, left out of the import (PRD §4.3 stage 6).
func markDuplicates(ctx context.Context, q db.Querier, userID uuid.UUID, rows []db.InsertImportRowsParams) error {
	keys := make([]statements.DedupRow, len(rows))
	params := db.ListLedgerMatchesParams{UserID: userID}
	for i, row := range rows {
		keys[i].Hash = row.DedupHash
		if row.DedupHash != nil {
			params.Hashes = append(params.Hashes, row.DedupHash)
		}
		if row.ExternalRef != nil && row.DedupHash != nil {
			keys[i].ExternalRef = *row.ExternalRef
			params.ExternalRefs = append(params.ExternalRefs, *row.ExternalRef)
		}
	}
	if len(params.Hashes) == 0 {
		return nil
	}
	found, err := q.ListLedgerMatches(ctx, params)
	if err != nil {
		return fmt.Errorf("imports: ledger matches: %w", err)
	}
	ledger := make([]statements.LedgerEntry, len(found))
	for i, tx := range found {
		ledger[i] = statements.LedgerEntry{ID: tx.ID, Hash: tx.DedupHash}
		if tx.ExternalRef != nil {
			ledger[i].ExternalRef = *tx.ExternalRef
		}
	}
	for i, match := range statements.Dedup(keys, ledger) {
		if match != uuid.Nil {
			rows[i].Status, rows[i].Included = "duplicate", false
			rows[i].DuplicateOf = &match
		}
	}
	return nil
}

// summarize counts an import's rows; the totals are of the included ones.
func summarize(rows []db.InsertImportRowsParams) Summary {
	var summary Summary
	for _, row := range rows {
		summary.RowCount++
		switch row.Status {
		case "error":
			summary.ErrorCount++
		case "duplicate":
			summary.DuplicateCount++
		}
		if !row.Included {
			continue
		}
		summary.IncludedCount++
		if *row.Kind == string(transactions.KindIncome) {
			summary.TotalIncomeMinor += *row.AmountMinor
			continue
		}
		summary.TotalExpenseMinor += *row.AmountMinor
		if row.CategoryID == nil {
			summary.UncategorizedCount++
		}
	}
	return summary
}

func jsonOrNil(v any) ([]byte, error) {
	switch v := v.(type) {
	case *statements.MappingSpec:
		if v == nil {
			return nil, nil
		}
	case *statements.MappingRequest:
		if v == nil {
			return nil, nil
		}
	}
	return json.Marshal(v)
}

// fileFailure is the failure for an error reading the file.
func fileFailure(err error) outcome {
	switch {
	case errors.Is(err, statements.ErrEmptyFile), errors.Is(err, statements.ErrNoRows):
		return failed(FailureEmptyFile, "The statement has no transactions.")
	case errors.Is(err, statements.ErrUnsupportedFormat):
		return failed(FailureUnsupportedFormat, "This file is not a statement Sprout can read: use CSV, OFX, XLSX or PDF.")
	case errors.Is(err, statements.ErrTooManyRows):
		return failed(FailureTooManyRows, "The statement is too long. Export a shorter period.")
	case errors.Is(err, statements.ErrLowParseRate):
		return failed(FailureUnreadable, "Most of this statement's rows could not be read.")
	}
	return failed(FailureUnreadable, "This statement could not be read.")
}

func failed(code FailureCode, message string) outcome {
	return outcome{status: StatusFailed, failure: &Failure{Code: code, Message: message}}
}

func withFormat(o outcome, format statements.Format) outcome {
	o.format = format
	return o
}

// Fail marks an import failed, as the job does once its attempts run out.
func Fail(ctx context.Context, q db.Querier, importID uuid.UUID, failure Failure) error {
	return fail(ctx, q, importID, failure)
}

func fail(ctx context.Context, q db.Querier, importID uuid.UUID, failure Failure) error {
	encoded, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("imports: failure: %w", err)
	}
	if err := q.FailImport(ctx, db.FailImportParams{ID: importID, Failure: encoded}); err != nil {
		return fmt.Errorf("imports: fail: %w", err)
	}
	return nil
}
