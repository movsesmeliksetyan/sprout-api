package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// Status is where an import is in its life (contract §1.3).
type Status string

// The statuses of an import.
const (
	StatusProcessing   Status = "processing"
	StatusNeedsMapping Status = "needs_mapping"
	StatusReady        Status = "ready"
	StatusFailed       Status = "failed"
	StatusCommitted    Status = "committed"
)

// FailureCode says why an import failed.
type FailureCode string

// The failure codes of contract §2.10.
const (
	FailureUnsupportedFormat FailureCode = "unsupported_format"
	FailureEmptyFile         FailureCode = "empty_file"
	FailureUnreadable        FailureCode = "unreadable"
	FailureTooManyRows       FailureCode = "too_many_rows"
	FailureInternal          FailureCode = "internal"
)

// Failure is an import's failure as stored and sent.
type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message"`
}

// Summary is an import's row counts and totals; all zero while processing.
type Summary struct {
	RowCount           int   `json:"row_count"`
	IncludedCount      int   `json:"included_count"`
	TotalExpenseMinor  int64 `json:"total_expense_minor"`
	TotalIncomeMinor   int64 `json:"total_income_minor"`
	DuplicateCount     int   `json:"duplicate_count"`
	UncategorizedCount int   `json:"uncategorized_count"`
	ErrorCount         int   `json:"error_count"`
	OutOfPeriodCount   int   `json:"out_of_period_count"`
}

// Periods are the windows an import can keep.
var periods = map[string]bool{"week": true, "month": true, "year": true, "all": true}

// Enqueuer starts processing an import in the background, in the
// transaction that creates it, so an import is never left without a job;
// and checks the budgets once a commit has added its expenses, with one
// combined alert.
type Enqueuer interface {
	EnqueueImport(ctx context.Context, tx pgx.Tx, importID uuid.UUID) error
	EnqueueBudgetCheck(ctx context.Context, userID uuid.UUID, importID *uuid.UUID)
}

// Database is what the service needs from the pool: queries, and
// transactions for the writes.
type Database interface {
	db.DBTX
	db.Beginner
}

// Service holds the business logic for imports.
type Service struct {
	db      Database
	uploads *uploads.Service
	jobs    Enqueuer
}

// NewService returns a Service working on database, claiming statements
// from uploads and processing them through jobs.
func NewService(database Database, uploadService *uploads.Service, jobs Enqueuer) *Service {
	return &Service{db: database, uploads: uploadService, jobs: jobs}
}

// Create starts an import of the user's statement upload, keeping the
// period's window of transactions. The upload is used up; processing
// starts in the background.
func (s *Service) Create(ctx context.Context, userID, uploadID uuid.UUID, period string) (db.Import, error) {
	if !periods[period] {
		return db.Import{}, &httpx.ValidationError{Fields: map[string]string{"period": "must be week, month, year or all"}}
	}
	var created db.Import
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		upload, err := s.uploads.Consume(ctx, q, userID, uploadID, uploads.PurposeStatement)
		if reason, refused := uploads.Rejection(err); refused {
			return &httpx.ValidationError{Fields: map[string]string{"upload_id": reason}}
		}
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("imports: new id: %w", err)
		}
		created, err = q.CreateImport(ctx, db.CreateImportParams{
			ID: id, UserID: userID, UploadID: upload.ID, Period: period, Filename: upload.Filename,
		})
		if err != nil {
			return fmt.Errorf("imports: create: %w", err)
		}
		if err := keepFile(ctx, q, upload.ID, AbandonedFileTTL); err != nil {
			return err
		}
		return s.jobs.EnqueueImport(ctx, tx, created.ID)
	})
	if err != nil {
		return db.Import{}, err
	}
	return created, nil
}

// Get returns one of the user's imports; another user's is not found.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (db.Import, error) {
	found, err := db.New(s.db).GetImport(ctx, db.GetImportParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Import{}, httpx.ErrNotFound
	}
	if err != nil {
		return db.Import{}, fmt.Errorf("imports: get: %w", err)
	}
	return found, nil
}

// Delete discards an import and its rows; its statement file goes after
// ClosedFileTTL. A committed import is the ledger's now and cannot be
// discarded.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetImportForUpdate(ctx, db.GetImportForUpdateParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("imports: delete: %w", err)
		}
		if Status(found.Status) == StatusCommitted {
			return httpx.ErrConflict
		}
		if err := keepFile(ctx, q, found.UploadID, ClosedFileTTL); err != nil {
			return err
		}
		if _, err := q.DeleteImport(ctx, db.DeleteImportParams{ID: id, UserID: userID}); err != nil {
			return fmt.Errorf("imports: delete: %w", err)
		}
		return nil
	})
}

// How long a statement file is kept (PRD §4.5).
const (
	// AbandonedFileTTL is from the import's creation, for an import that
	// is never committed or discarded.
	AbandonedFileTTL = 7 * 24 * time.Hour
	// ClosedFileTTL is from its commit or discard.
	ClosedFileTTL = 24 * time.Hour
)

// keepFile sets when the statement file of upload is deleted from storage:
// ttl from now.
func keepFile(ctx context.Context, q db.Querier, uploadID uuid.UUID, ttl time.Duration) error {
	if err := q.SetUploadPurgeAfter(ctx, db.SetUploadPurgeAfterParams{ID: uploadID, PurgeAfter: ptr(time.Now().Add(ttl))}); err != nil {
		return fmt.Errorf("imports: file retention: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// summaryOf reads an import's stored summary.
func summaryOf(row db.Import) (Summary, error) {
	var summary Summary
	if len(row.Summary) == 0 {
		return summary, nil
	}
	if err := json.Unmarshal(row.Summary, &summary); err != nil {
		return Summary{}, fmt.Errorf("imports: summary: %w", err)
	}
	return summary, nil
}
