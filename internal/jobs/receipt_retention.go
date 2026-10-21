package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
)

// PurgeReceiptsArgs deletes the receipts not confirmed within a week. It
// runs hourly.
type PurgeReceiptsArgs struct{}

// Kind names the job.
func (PurgeReceiptsArgs) Kind() string { return "purge_receipts" }

type purgeReceipts struct {
	river.WorkerDefaults[PurgeReceiptsArgs]
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func (w *purgeReceipts) Work(ctx context.Context, _ *river.Job[PurgeReceiptsArgs]) error {
	purged, err := receipts.PurgeUnconfirmed(ctx, db.New(w.pool), time.Now())
	if purged > 0 {
		w.logger.InfoContext(ctx, "unconfirmed receipts purged", "purged", purged)
	}
	return err
}
