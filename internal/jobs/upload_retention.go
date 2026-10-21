package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// PurgeUploadFilesArgs deletes the uploaded files whose retention has run
// out: statements (PRD §4.5) and receipt photos. It runs hourly.
type PurgeUploadFilesArgs struct{}

// Kind names the job.
func (PurgeUploadFilesArgs) Kind() string { return "purge_upload_files" }

type purgeUploadFiles struct {
	river.WorkerDefaults[PurgeUploadFilesArgs]
	pool   *pgxpool.Pool
	store  storage.Store
	logger *slog.Logger
}

func (w *purgeUploadFiles) Work(ctx context.Context, _ *river.Job[PurgeUploadFilesArgs]) error {
	purged, err := uploads.PurgeFiles(ctx, db.New(w.pool), w.store, time.Now())
	if purged > 0 {
		w.logger.InfoContext(ctx, "uploaded files purged", "purged", purged)
	}
	return err
}
