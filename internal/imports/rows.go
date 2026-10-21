package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

const (
	// DefaultRowLimit is the page of rows when the request names none.
	DefaultRowLimit = 50
	// MaxRowLimit is the largest page of rows a request can ask for.
	MaxRowLimit = 200
)

// The statuses of an import row (contract §1.3).
const (
	RowOK        = "ok"
	RowDuplicate = "duplicate"
	RowError     = "error"
)

// KindTransfer is an import row that moves the user's own money: it never
// reaches the ledger.
const KindTransfer = string(categorize.KindTransfer)

// userConfidence is the confidence of a category the user chose.
const userConfidence = 1.0

// RowCursor is a position in an import's rows: the rank (0 for an included
// expense without a category, else 1), the local date and the id of the
// last row of a page.
type RowCursor struct {
	Rank   int32
	Cursor httpx.Cursor
}

// EncodeRowCursor is the opaque form of c sent as next_cursor.
func EncodeRowCursor(c RowCursor) string {
	return strconv.Itoa(int(c.Rank)) + httpx.EncodeCursor(c.Cursor)
}

// DecodeRowCursor parses a cursor received from a client; anything
// EncodeRowCursor could not have produced is httpx.ErrBadRequest.
func DecodeRowCursor(s string) (RowCursor, error) {
	if len(s) < 2 || (s[0] != '0' && s[0] != '1') {
		return RowCursor{}, httpx.WithMessage(httpx.ErrBadRequest, "The cursor is not valid.")
	}
	cursor, err := httpx.DecodeCursor(s[1:])
	if err != nil {
		return RowCursor{}, err
	}
	return RowCursor{Rank: int32(s[0] - '0'), Cursor: cursor}, nil
}

// RowPage is a page of an import's rows.
type RowPage struct {
	Items []db.ImportRow
	Next  *RowCursor
}

// ListRows returns a page of one of the user's imports' rows: the included
// expenses still without a category first, then newest first. A nil limit
// is DefaultRowLimit, and a nil after is the first page.
func (s *Service) ListRows(ctx context.Context, userID, importID uuid.UUID, limit *int, after *RowCursor) (RowPage, error) {
	size := DefaultRowLimit
	if limit != nil {
		size = *limit
	}
	if size < 1 || size > MaxRowLimit {
		return RowPage{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The limit must be between 1 and %d.", MaxRowLimit))
	}
	if _, err := s.Get(ctx, userID, importID); err != nil {
		return RowPage{}, err
	}
	params := db.ListImportRowsParams{ImportID: importID, UserID: userID, RowLimit: int32(size) + 1}
	if after != nil {
		params.CursorRank = after.Rank
		params.CursorDate, params.CursorID = &after.Cursor.LocalDate, &after.Cursor.ID
	}
	found, err := db.New(s.db).ListImportRows(ctx, params)
	if err != nil {
		return RowPage{}, fmt.Errorf("imports: list rows: %w", err)
	}
	var page RowPage
	for i, row := range found {
		if i == size {
			last := found[size-1]
			page.Next = &RowCursor{Rank: last.SortRank, Cursor: httpx.Cursor{LocalDate: last.SortDate, ID: last.ID}}
			break
		}
		page.Items = append(page.Items, importRowOf(row))
	}
	return page, nil
}

func importRowOf(r db.ListImportRowsRow) db.ImportRow {
	return db.ImportRow{
		ID: r.ID, ImportID: r.ImportID, UserID: r.UserID, RowIndex: r.RowIndex, Raw: r.Raw, Status: r.Status,
		Included: r.Included, Kind: r.Kind, AmountMinor: r.AmountMinor, OccurredAt: r.OccurredAt, LocalDate: r.LocalDate,
		Merchant: r.Merchant, MerchantKey: r.MerchantKey, RawDescription: r.RawDescription, Mcc: r.Mcc,
		ExternalRef: r.ExternalRef, CategoryID: r.CategoryID, CategoryConfidence: r.CategoryConfidence,
		CategorySource: r.CategorySource, DedupHash: r.DedupHash, DuplicateOf: r.DuplicateOf, Error: r.Error,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Note: r.Note,
	}
}

// RowPatch is an edit of an import row; nil fields stay as they are.
type RowPatch struct {
	CategoryID *uuid.UUID
	Included   *bool
	Kind       *string
	Merchant   *string
}

// UpdateRow edits a row of one of the user's imports while it is ready for
// review, and recounts the import's summary, in one transaction. Choosing a
// category teaches the categoriser: the merchant is the user's own rule
// from then on (categorize.Learn). A row that is not readable cannot be
// included or given a kind, a transfer cannot be included, and only an
// expense has a category — one of the user's active ones; an expense that
// becomes something else loses it. An import that is not ready is
// httpx.ErrConflict; a row that is missing or someone else's is
// httpx.ErrNotFound.
func (s *Service) UpdateRow(ctx context.Context, userID, importID, rowID uuid.UUID, patch RowPatch) (db.ImportRow, error) {
	var updated db.ImportRow
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetImportForUpdate(ctx, db.GetImportForUpdateParams{ID: importID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("imports: update row: %w", err)
		}
		row, err := q.GetImportRowForUpdate(ctx, db.GetImportRowForUpdateParams{ID: rowID, ImportID: importID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("imports: update row: %w", err)
		}
		if Status(found.Status) != StatusReady {
			return httpx.ErrConflict
		}
		params, learn, err := applyPatch(ctx, q, userID, row, patch)
		if err != nil {
			return err
		}
		if updated, err = q.UpdateImportRow(ctx, params); err != nil {
			return fmt.Errorf("imports: update row: %w", err)
		}
		if learn && updated.CategoryID != nil {
			if err := categorize.Learn(ctx, q, userID, updated.MerchantKey, *updated.CategoryID); err != nil {
				return err
			}
		}
		return recount(ctx, q, found)
	})
	if err != nil {
		return db.ImportRow{}, err
	}
	return updated, nil
}

// applyPatch is the row after the patch, and whether a category was
// chosen.
func applyPatch(ctx context.Context, q db.Querier, userID uuid.UUID, row db.ImportRow, patch RowPatch) (db.UpdateImportRowParams, bool, error) {
	params := db.UpdateImportRowParams{
		ID: row.ID, Included: row.Included, Kind: row.Kind, Merchant: row.Merchant, MerchantKey: row.MerchantKey,
		Note: row.Note, CategoryID: row.CategoryID, CategoryConfidence: row.CategoryConfidence,
		CategorySource: row.CategorySource, DedupHash: row.DedupHash,
	}
	fields := map[string]string{}
	readable := row.Status != RowError
	if patch.Kind != nil {
		switch {
		case !readable:
			fields["kind"] = "cannot be set on a row that could not be read"
		case *patch.Kind != string(transactions.KindExpense) && *patch.Kind != string(transactions.KindIncome) && *patch.Kind != KindTransfer:
			fields["kind"] = "must be expense, income or transfer"
		default:
			params.Kind = patch.Kind
		}
	}
	if patch.Merchant != nil {
		merchant := strings.TrimSpace(*patch.Merchant)
		switch {
		case !readable:
			fields["merchant"] = "cannot be set on a row that could not be read"
		case utf8.RuneCountInString(merchant) > transactions.MaxMerchantLength:
			fields["merchant"] = fmt.Sprintf("must be at most %d characters", transactions.MaxMerchantLength)
		case merchant == "":
			params.Merchant, params.MerchantKey = nil, categorize.Normalize(row.RawDescription).Key
		default:
			params.Merchant, params.MerchantKey = &merchant, transactions.MerchantKey(merchant)
		}
	}
	isExpense := params.Kind != nil && *params.Kind == string(transactions.KindExpense)
	if patch.Included != nil {
		params.Included = *patch.Included
	} else if patch.Kind != nil && *patch.Kind == KindTransfer {
		params.Included = false
	}
	switch {
	case params.Included && !readable:
		fields["included"] = "a row that could not be read cannot be included"
	case params.Included && params.Kind != nil && *params.Kind == KindTransfer:
		fields["included"] = "a transfer is not imported; change its kind to include it"
	}
	learn := false
	switch {
	case patch.CategoryID != nil && !isExpense:
		fields["category_id"] = "only an expense has a category"
	case patch.CategoryID != nil:
		category, err := q.GetCategory(ctx, db.GetCategoryParams{ID: *patch.CategoryID, UserID: userID})
		switch {
		case errors.Is(err, pgx.ErrNoRows) || (err == nil && category.ArchivedAt != nil):
			fields["category_id"] = "must be one of your active categories"
		case err != nil:
			return params, false, fmt.Errorf("imports: category: %w", err)
		default:
			confidence, source := userConfidence, string(categorize.SourceUserRule)
			params.CategoryID, params.CategoryConfidence, params.CategorySource = &category.ID, &confidence, &source
			learn = true
		}
	case !isExpense && params.CategoryID != nil:
		none := string(categorize.SourceNone)
		params.CategoryID, params.CategoryConfidence, params.CategorySource = nil, nil, &none
	}
	if len(fields) > 0 {
		return params, false, &httpx.ValidationError{Fields: fields}
	}
	if readable && params.Kind != nil && *params.Kind != KindTransfer {
		params.DedupHash = transactions.DedupHash(*row.LocalDate, *row.AmountMinor, transactions.Kind(*params.Kind), params.MerchantKey)
	}
	return params, learn, nil
}

// recount stores the import's summary as its rows now stand.
func recount(ctx context.Context, q db.Querier, found db.Import) error {
	previous, err := summaryOf(found)
	if err != nil {
		return err
	}
	summary, err := countRows(ctx, q, found.ID)
	if err != nil {
		return err
	}
	summary.OutOfPeriodCount = previous.OutOfPeriodCount
	encoded, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if err := q.SetImportSummary(ctx, db.SetImportSummaryParams{ID: found.ID, Summary: encoded}); err != nil {
		return fmt.Errorf("imports: summary: %w", err)
	}
	return nil
}
