package jobs

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

// PurgeIdempotencyKeysArgs deletes expired idempotency keys. It runs
// hourly.
type PurgeIdempotencyKeysArgs struct{}

// Kind names the job.
func (PurgeIdempotencyKeysArgs) Kind() string { return "purge_idempotency_keys" }

type purgeIdempotencyKeys struct {
	river.WorkerDefaults[PurgeIdempotencyKeysArgs]
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func (w *purgeIdempotencyKeys) Work(ctx context.Context, _ *river.Job[PurgeIdempotencyKeysArgs]) error {
	removed, err := httpx.PurgeIdempotencyKeys(ctx, w.pool)
	if err != nil {
		return err
	}
	if removed > 0 {
		w.logger.InfoContext(ctx, "idempotency keys purged", "removed", removed)
	}
	return nil
}
