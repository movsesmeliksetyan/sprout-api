package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/insights"
)

// activeFor is how recently a user must have added a transaction for the
// nightly job to compute their insights ahead of time.
const activeFor = 30 * 24 * time.Hour

// RefreshInsightsArgs computes the current month's insights of the users
// active lately, so that their next read is served from the table. It runs
// nightly.
type RefreshInsightsArgs struct{}

// Kind names the job.
func (RefreshInsightsArgs) Kind() string { return "refresh_insights" }

type refreshInsights struct {
	river.WorkerDefaults[RefreshInsightsArgs]
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func (w *refreshInsights) Work(ctx context.Context, _ *river.Job[RefreshInsightsArgs]) error {
	users, err := db.New(w.pool).ActiveInsightUsers(ctx, time.Now().Add(-activeFor))
	if err != nil {
		return fmt.Errorf("jobs: active users: %w", err)
	}
	service := insights.NewService(w.pool, goals.NewService(w.pool))
	failed := 0
	for _, user := range users {
		if err := service.Refresh(ctx, user); err != nil {
			failed++
			w.logger.WarnContext(ctx, "insights refresh failed", "user_id", user.ID, "error", err.Error())
		}
	}
	w.logger.InfoContext(ctx, "insights refreshed", "users", len(users), "failed", failed)
	return nil
}
