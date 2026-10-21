package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
)

// budgetCheckWindow debounces budget checks: the writes of one user in the
// same window share one check, run budgetCheckSettle after the window
// ends so that the window's writes have committed. A write after that falls
// in a later window and gets a check of its own, so none is missed.
const (
	budgetCheckWindow = 10 * time.Second
	budgetCheckSettle = 2 * time.Second
)

// CheckBudgetsArgs alerts a user about budgets reached this month.
type CheckBudgetsArgs struct {
	UserID uuid.UUID `json:"user_id"`
	// ImportID is the committed import that asked, whose alerts are
	// combined into one; nil for any other write.
	ImportID *uuid.UUID `json:"import_id"`
	// Window numbers the debounce window, making the job unique per
	// window.
	Window int64 `json:"window"`
}

// Kind names the job.
func (CheckBudgetsArgs) Kind() string { return "budget_check" }

// InsertOpts makes the job unique per user, import and window.
func (CheckBudgetsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EnqueueBudgetCheck schedules a check of the user's budgets after a write
// that added spending, debounced; importID names an import commit. It runs
// after the write has committed, so a failure is logged rather than
// undoing the write.
func (c *Client) EnqueueBudgetCheck(ctx context.Context, userID uuid.UUID, importID *uuid.UUID) {
	window := time.Now().UnixNano() / int64(budgetCheckWindow)
	at := time.Unix(0, (window+1)*int64(budgetCheckWindow)).Add(budgetCheckSettle)
	args := CheckBudgetsArgs{UserID: userID, ImportID: importID, Window: window}
	if _, err := c.river.Insert(ctx, args, &river.InsertOpts{ScheduledAt: at}); err != nil {
		c.logger.ErrorContext(ctx, "enqueue budget check failed", "user_id", userID, "error", err.Error())
	}
}

// SpendChanged schedules a budget check after an expense is saved:
// transactions.SpendHook.
func (c *Client) SpendChanged(ctx context.Context, userID uuid.UUID) {
	c.EnqueueBudgetCheck(ctx, userID, nil)
}

type checkBudgets struct {
	river.WorkerDefaults[CheckBudgetsArgs]
	logger  *slog.Logger
	service *notify.Service
}

func (w *checkBudgets) Work(ctx context.Context, job *river.Job[CheckBudgetsArgs]) error {
	sent, err := w.service.CheckBudgets(ctx, job.Args.UserID, job.Args.ImportID)
	if err != nil {
		return err
	}
	if sent > 0 {
		w.logger.InfoContext(ctx, "budget alerts sent", "user_id", job.Args.UserID, "notifications", sent)
	}
	return nil
}
