package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
)

// SendWeeklyRecapsArgs sends last week's recap to the users for whom it is
// Monday 09:00–09:59. It runs hourly.
type SendWeeklyRecapsArgs struct{}

// Kind names the job.
func (SendWeeklyRecapsArgs) Kind() string { return "weekly_recap" }

type sendWeeklyRecaps struct {
	river.WorkerDefaults[SendWeeklyRecapsArgs]
	logger  *slog.Logger
	service *notify.Service
}

func (w *sendWeeklyRecaps) Work(ctx context.Context, _ *river.Job[SendWeeklyRecapsArgs]) error {
	result, err := w.service.WeeklyRecaps(ctx, time.Now())
	w.logger.InfoContext(ctx, "weekly recaps", "sent", result.Sent, "skipped", result.Skipped)
	return err
}

// hourly schedules a periodic job a minute past every hour, so that each
// run falls inside one local hour of every timezone, half-hour ones
// included.
type hourly struct{}

func (hourly) Next(current time.Time) time.Time {
	next := current.UTC().Truncate(time.Hour).Add(time.Minute)
	if !next.After(current) {
		next = next.Add(time.Hour)
	}
	return next
}
