package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
)

// importAttempts is how often processing an import is tried before it is
// marked failed.
const importAttempts = 3

// ProcessImportArgs processes one import.
type ProcessImportArgs struct {
	ImportID uuid.UUID `json:"import_id"`
}

// Kind names the job.
func (ProcessImportArgs) Kind() string { return "process_import" }

// InsertOpts bounds the retries.
func (ProcessImportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: importAttempts}
}

// EnqueueImport inserts the import's job in tx, the transaction that
// creates or resets the import.
func (c *Client) EnqueueImport(ctx context.Context, tx pgx.Tx, importID uuid.UUID) error {
	if _, err := c.river.InsertTx(ctx, tx, ProcessImportArgs{ImportID: importID}, nil); err != nil {
		return fmt.Errorf("jobs: enqueue import: %w", err)
	}
	return nil
}

var _ imports.Enqueuer = (*Client)(nil)

type processImport struct {
	river.WorkerDefaults[ProcessImportArgs]
	pool      *pgxpool.Pool
	logger    *slog.Logger
	processor *imports.Processor
}

// Work runs the import's pipeline. When the last attempt fails too, the
// import is marked failed so the app stops polling.
func (w *processImport) Work(ctx context.Context, job *river.Job[ProcessImportArgs]) error {
	err := w.processor.Process(ctx, job.Args.ImportID)
	if err == nil || job.Attempt < job.MaxAttempts {
		return err
	}
	w.logger.ErrorContext(ctx, "import failed", "import_id", job.Args.ImportID, "error", err.Error())
	failure := imports.Failure{Code: imports.FailureInternal, Message: "Something went wrong reading this statement."}
	if markErr := imports.Fail(ctx, db.New(w.pool), job.Args.ImportID, failure); markErr != nil {
		return fmt.Errorf("%w (and marking it failed: %w)", err, markErr)
	}
	return nil
}
