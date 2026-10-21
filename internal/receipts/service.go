package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// Status is where a receipt is in its life (contract §1.3).
type Status string

// The statuses of a receipt.
const (
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
	StatusConfirmed  Status = "confirmed"
)

// FailureCode says why a receipt scan failed.
type FailureCode string

// The failure codes of contract §2.11.
const (
	FailureNotAReceipt FailureCode = "not_a_receipt"
	FailureUnreadable  FailureCode = "unreadable"
	FailureInternal    FailureCode = "internal"
)

// Failure is a receipt's failure as stored and sent.
type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message"`
}

// LineItem is one line of a receipt as stored and sent.
type LineItem struct {
	Name        string   `json:"name"`
	Quantity    *float64 `json:"quantity"`
	AmountMinor *int64   `json:"amount_minor"`
}

const (
	// imageURLLifetime is how long a receipt's image link works.
	imageURLLifetime = time.Hour
	// UnconfirmedTTL is how long the image of a receipt that is never
	// confirmed is kept.
	UnconfirmedTTL = 7 * 24 * time.Hour
)

// Enqueuer starts processing a receipt in the background, in the
// transaction that creates it.
type Enqueuer interface {
	EnqueueReceipt(ctx context.Context, tx pgx.Tx, receiptID uuid.UUID) error
}

// Database is what the service needs from the pool: queries, and
// transactions for the writes.
type Database interface {
	db.DBTX
	db.Beginner
}

// Service holds the business logic for receipts.
type Service struct {
	db      Database
	uploads *uploads.Service
	store   storage.Store
	jobs    Enqueuer
}

// NewService returns a Service working on database, claiming images from
// uploads, linking to them in store and processing them through jobs.
func NewService(database Database, uploadService *uploads.Service, store storage.Store, jobs Enqueuer) *Service {
	return &Service{db: database, uploads: uploadService, store: store, jobs: jobs}
}

// Receipt is a receipt with a link to its image, nil once the image is
// gone.
type Receipt struct {
	db.Receipt
	ImageURL *string
}

// Create starts scanning the user's receipt upload. The upload is used up;
// extraction starts in the background. The image is deleted after
// UnconfirmedTTL unless the receipt is confirmed.
func (s *Service) Create(ctx context.Context, userID, uploadID uuid.UUID) (Receipt, error) {
	var created db.Receipt
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		upload, err := s.uploads.Consume(ctx, q, userID, uploadID, uploads.PurposeReceipt)
		if reason, refused := uploads.Rejection(err); refused {
			return &httpx.ValidationError{Fields: map[string]string{"upload_id": reason}}
		}
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("receipts: new id: %w", err)
		}
		if created, err = q.CreateReceipt(ctx, db.CreateReceiptParams{ID: id, UserID: userID, UploadID: upload.ID}); err != nil {
			return fmt.Errorf("receipts: create: %w", err)
		}
		purgeAfter := time.Now().Add(UnconfirmedTTL)
		if err := q.SetUploadPurgeAfter(ctx, db.SetUploadPurgeAfterParams{ID: upload.ID, PurgeAfter: &purgeAfter}); err != nil {
			return fmt.Errorf("receipts: image retention: %w", err)
		}
		return s.jobs.EnqueueReceipt(ctx, tx, created.ID)
	})
	if err != nil {
		return Receipt{}, err
	}
	return s.Get(ctx, userID, created.ID)
}

// Get returns one of the user's receipts; another user's is not found.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (Receipt, error) {
	found, err := db.New(s.db).GetReceipt(ctx, db.GetReceiptParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, httpx.ErrNotFound
	}
	if err != nil {
		return Receipt{}, fmt.Errorf("receipts: get: %w", err)
	}
	out := Receipt{Receipt: receiptOf(found)}
	if found.ImagePurgedAt == nil {
		url, err := s.store.PresignGet(ctx, found.ImageKey, imageURLLifetime)
		if err != nil {
			return Receipt{}, fmt.Errorf("receipts: image url: %w", err)
		}
		out.ImageURL = &url
	}
	return out, nil
}

func receiptOf(r db.GetReceiptRow) db.Receipt {
	return db.Receipt{
		ID: r.ID, UserID: r.UserID, UploadID: r.UploadID, Status: r.Status, Merchant: r.Merchant,
		PurchasedAt: r.PurchasedAt, TotalMinor: r.TotalMinor, Currency: r.Currency, PaymentMethod: r.PaymentMethod,
		CategoryID: r.CategoryID, CategoryConfidence: r.CategoryConfidence, LineItems: r.LineItems,
		RawExtraction: r.RawExtraction, TransactionID: r.TransactionID, Failure: r.Failure,
		ConfirmedAt: r.ConfirmedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// Delete removes a receipt that has not been confirmed; its image is
// deleted from storage by the next purge. A confirmed receipt belongs to
// its transaction and is httpx.ErrConflict.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetReceiptForUpdate(ctx, db.GetReceiptForUpdateParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("receipts: delete: %w", err)
		}
		if Status(found.Status) == StatusConfirmed {
			return httpx.WithMessage(httpx.ErrConflict, "A confirmed receipt stays with its transaction.")
		}
		now := time.Now()
		if err := q.SetUploadPurgeAfter(ctx, db.SetUploadPurgeAfterParams{ID: found.UploadID, PurgeAfter: &now}); err != nil {
			return fmt.Errorf("receipts: delete: %w", err)
		}
		if err := q.DeleteReceipt(ctx, id); err != nil {
			return fmt.Errorf("receipts: delete: %w", err)
		}
		return nil
	})
}

// Fail marks a receipt failed, as the job does once its attempts run out.
func Fail(ctx context.Context, q db.Querier, receiptID uuid.UUID, failure Failure) error {
	encoded, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("receipts: failure: %w", err)
	}
	if err := q.FailReceipt(ctx, db.FailReceiptParams{ID: receiptID, Failure: encoded}); err != nil {
		return fmt.Errorf("receipts: fail: %w", err)
	}
	return nil
}
