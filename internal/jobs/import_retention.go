package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/imports"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

// PurgeStatementFilesArgs deletes the statement files whose retention has
// run out (PRD §4.5). It runs hourly.
type PurgeStatementFilesArgs struct{}

// Kind names the job.
func (PurgeStatementFilesArgs) Kind() string { return "purge_statement_files" }

type purgeStatementFiles struct {
	river.WorkerDefaults[PurgeStatementFilesArgs]
	pool   *pgxpool.Pool
	store  storage.Store
	logger *slog.Logger
}

func (w *purgeStatementFiles) Work(ctx context.Context, _ *river.Job[PurgeStatementFilesArgs]) error {
	purged, err := imports.PurgeFiles(ctx, db.New(w.pool), w.store, time.Now())
	if purged > 0 {
		w.logger.InfoContext(ctx, "statement files purged", "purged", purged)
	}
	return err
}
