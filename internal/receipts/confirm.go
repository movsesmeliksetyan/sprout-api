package receipts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// Confirmation is what the user settled for a receipt; Merchant and Note
// may be nil.
type Confirmation struct {
	AmountMinor int64
	CategoryID  uuid.UUID
	OccurredAt  time.Time
	Merchant    *string
	Note        *string
}

// Confirm writes a scanned receipt to the user's ledger as an expense, in
// one transaction: the transaction (source receipt, with the receipt's id,
// validated as a manual one is, and teaching the categoriser the merchant's
// category), and the receipt confirmed, its photo kept for the transaction.
// Confirming again returns the same transaction and creates nothing. A
// receipt still processing is httpx.ErrConflict; one whose scan failed can
// be confirmed with what the user typed.
func (s *Service) Confirm(ctx context.Context, user db.User, id uuid.UUID, c Confirmation) (db.Transaction, error) {
	var created db.Transaction
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetReceiptForUpdate(ctx, db.GetReceiptForUpdateParams{ID: id, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("receipts: confirm: %w", err)
		}
		switch Status(found.Status) {
		case StatusConfirmed:
			if found.TransactionID == nil {
				return httpx.WithMessage(httpx.ErrConflict, "The transaction from this receipt was deleted.")
			}
			created, err = q.GetTransaction(ctx, db.GetTransactionParams{ID: *found.TransactionID, UserID: user.ID})
			if err != nil {
				return fmt.Errorf("receipts: confirmed transaction: %w", err)
			}
			return nil
		case StatusProcessing:
			return httpx.WithMessage(httpx.ErrConflict, "The receipt is still being read.")
		}

		created, err = s.ledger.CreateIn(ctx, q, user, transactions.CreateParams{
			Kind: transactions.KindExpense, AmountMinor: c.AmountMinor, CategoryID: &c.CategoryID,
			Merchant: c.Merchant, Note: c.Note, OccurredAt: &c.OccurredAt, ReceiptID: &found.ID,
		})
		if err != nil {
			return err
		}
		now := time.Now()
		if err := q.ConfirmReceipt(ctx, db.ConfirmReceiptParams{ID: id, TransactionID: &created.ID, ConfirmedAt: &now}); err != nil {
			return fmt.Errorf("receipts: confirm: %w", err)
		}
		// The photo stays with the transaction.
		if err := q.SetUploadPurgeAfter(ctx, db.SetUploadPurgeAfterParams{ID: found.UploadID}); err != nil {
			return fmt.Errorf("receipts: keep photo: %w", err)
		}
		return nil
	})
	if err != nil {
		return db.Transaction{}, err
	}
	return created, nil
}

// PurgeUnconfirmed deletes the receipts not confirmed within
// UnconfirmedTTL; their photos are deleted from storage by the uploads'
// purge, due at the same time. It returns how many went.
func PurgeUnconfirmed(ctx context.Context, q db.Querier, now time.Time) (int, error) {
	n, err := q.DeleteUnconfirmedReceipts(ctx, now.Add(-UnconfirmedTTL))
	if err != nil {
		return 0, fmt.Errorf("receipts: purge: %w", err)
	}
	return int(n), nil
}
