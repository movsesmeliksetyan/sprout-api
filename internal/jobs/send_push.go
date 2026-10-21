package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
)

// pushAttempts is how often delivering a notification is tried; it stays
// in the inbox if every attempt fails.
const pushAttempts = 5

// SendPushArgs delivers one stored notification to the user's devices.
type SendPushArgs struct {
	UserID         uuid.UUID `json:"user_id"`
	NotificationID uuid.UUID `json:"notification_id"`
}

// Kind names the job.
func (SendPushArgs) Kind() string { return "send_push" }

// InsertOpts bounds the retries.
func (SendPushArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: pushAttempts}
}

// EnqueuePush inserts the notification's delivery in tx, the transaction
// that stores it.
func (c *Client) EnqueuePush(ctx context.Context, tx pgx.Tx, userID, notificationID uuid.UUID) error {
	if _, err := c.river.InsertTx(ctx, tx, SendPushArgs{UserID: userID, NotificationID: notificationID}, nil); err != nil {
		return fmt.Errorf("jobs: enqueue push: %w", err)
	}
	return nil
}

var _ notify.Enqueuer = (*Client)(nil)

type sendPush struct {
	river.WorkerDefaults[SendPushArgs]
	logger  *slog.Logger
	service *notify.Service
}

func (w *sendPush) Work(ctx context.Context, job *river.Job[SendPushArgs]) error {
	d, err := w.service.Deliver(ctx, job.Args.UserID, job.Args.NotificationID)
	if err != nil {
		return err
	}
	if d.Invalidated > 0 || d.Failed > 0 {
		w.logger.InfoContext(ctx, "push partly delivered", "notification_id", job.Args.NotificationID,
			"delivered", d.Delivered, "invalidated", d.Invalidated, "failed", d.Failed)
	}
	return nil
}
