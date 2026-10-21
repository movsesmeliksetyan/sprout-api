package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
)

// PromoteDictionaryArgs recomputes the global merchant dictionary from
// users' votes (PRD §5.3). It runs nightly.
type PromoteDictionaryArgs struct{}

// Kind names the job.
func (PromoteDictionaryArgs) Kind() string { return "promote_dictionary" }

type promoteDictionary struct {
	river.WorkerDefaults[PromoteDictionaryArgs]
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func (w *promoteDictionary) Work(ctx context.Context, _ *river.Job[PromoteDictionaryArgs]) error {
	result, err := categorize.PromoteDictionary(ctx, w.pool, time.Now())
	if err != nil {
		return err
	}
	w.logger.InfoContext(ctx, "dictionary promoted", "promoted", result.Promoted, "demoted", result.Demoted, "expired", result.Expired)
	return nil
}
