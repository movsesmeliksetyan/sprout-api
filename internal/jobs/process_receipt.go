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
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
)

// receiptAttempts is how often reading a receipt is tried before it is
// marked failed.
const receiptAttempts = 3

// ProcessReceiptArgs reads one receipt.
type ProcessReceiptArgs struct {
	ReceiptID uuid.UUID `json:"receipt_id"`
}

// Kind names the job.
func (ProcessReceiptArgs) Kind() string { return "process_receipt" }

// InsertOpts bounds the retries.
func (ProcessReceiptArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: receiptAttempts}
}

// EnqueueReceipt inserts the receipt's job in tx, the transaction that
// creates it.
func (c *Client) EnqueueReceipt(ctx context.Context, tx pgx.Tx, receiptID uuid.UUID) error {
	if _, err := c.river.InsertTx(ctx, tx, ProcessReceiptArgs{ReceiptID: receiptID}, nil); err != nil {
		return fmt.Errorf("jobs: enqueue receipt: %w", err)
	}
	return nil
}

var _ receipts.Enqueuer = (*Client)(nil)

type processReceipt struct {
	river.WorkerDefaults[ProcessReceiptArgs]
	pool      *pgxpool.Pool
	logger    *slog.Logger
	processor *receipts.Processor
}

// Work reads the receipt. When the last attempt fails too, the receipt is
// marked failed so the app stops polling.
func (w *processReceipt) Work(ctx context.Context, job *river.Job[ProcessReceiptArgs]) error {
	err := w.processor.Process(ctx, job.Args.ReceiptID)
	if err == nil || job.Attempt < job.MaxAttempts {
		return err
	}
	w.logger.ErrorContext(ctx, "receipt failed", "receipt_id", job.Args.ReceiptID, "error", err.Error())
	failure := receipts.Failure{Code: receipts.FailureInternal, Message: "Something went wrong reading this receipt."}
	if markErr := receipts.Fail(ctx, db.New(w.pool), job.Args.ReceiptID, failure); markErr != nil {
		return fmt.Errorf("%w (and marking it failed: %w)", err, markErr)
	}
	return nil
}
