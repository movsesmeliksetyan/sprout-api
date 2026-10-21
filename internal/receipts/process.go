package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

// MaxImageBytes is the largest photo read, the upload limit.
const MaxImageBytes = 10 << 20

// Processor extracts receipts in the background (PRD BE-51).
type Processor struct {
	db     Database
	store  storage.Store
	llm    llm.Client
	engine *categorize.Engine
}

// NewProcessor returns a processor reading photos from store with the
// vision model of client, and filing them with engine; a nil engine is one
// without a language model.
func NewProcessor(database Database, store storage.Store, client llm.Client, engine *categorize.Engine) *Processor {
	if engine == nil {
		engine = categorize.NewEngine(db.New(database))
	}
	return &Processor{db: database, store: store, llm: client, engine: engine}
}

// Process extracts a receipt that is still processing; a receipt deleted,
// confirmed or already read meanwhile is left alone. A photo that cannot be
// read, or is not of a receipt, ends the receipt as failed; an error is
// returned only for what a retry may fix.
func (p *Processor) Process(ctx context.Context, receiptID uuid.UUID) error {
	q := db.New(p.db)
	source, err := q.GetReceiptSource(ctx, receiptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted
	}
	if err != nil {
		return fmt.Errorf("receipts: process: %w", err)
	}
	if Status(source.Status) != StatusProcessing {
		return nil
	}
	if p.llm == nil {
		return Fail(ctx, q, receiptID, Failure{Code: FailureInternal, Message: "Receipt scanning is not set up."})
	}
	loc, err := time.LoadLocation(source.Timezone)
	if err != nil {
		loc = time.UTC
	}

	data, err := p.store.Get(ctx, source.ObjectKey, MaxImageBytes)
	switch {
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, storage.ErrTooLarge):
		return Fail(ctx, q, receiptID, Failure{Code: FailureUnreadable, Message: "The photo is gone. Take it again."})
	case err != nil:
		return fmt.Errorf("receipts: read photo: %w", err)
	}
	img, err := NormalizeImage(data)
	if err != nil {
		return Fail(ctx, q, receiptID, Failure{Code: FailureUnreadable, Message: "This photo could not be read."})
	}
	extracted, err := Extract(ctx, p.llm, p.engine, source.UserID, img, source.Currency, loc)
	switch {
	case errors.Is(err, llm.ErrUnavailable):
		return fmt.Errorf("receipts: extract: %w", err)
	case err != nil:
		return Fail(ctx, q, receiptID, Failure{Code: FailureUnreadable, Message: "This receipt could not be read. Try a sharper photo."})
	case !extracted.IsReceipt:
		return Fail(ctx, q, receiptID, Failure{Code: FailureNotAReceipt, Message: "This photo does not look like a receipt."})
	}
	return finish(ctx, q, receiptID, extracted)
}

func finish(ctx context.Context, q db.Querier, receiptID uuid.UUID, e Extracted) error {
	items := e.LineItems
	if items == nil {
		items = []LineItem{}
	}
	lineItems, err := json.Marshal(items)
	if err != nil {
		return err
	}
	err = q.FinishReceipt(ctx, db.FinishReceiptParams{
		ID: receiptID, Merchant: e.Merchant, PurchasedAt: e.PurchasedAt, TotalMinor: e.TotalMinor,
		Currency: e.Currency, PaymentMethod: e.PaymentMethod, CategoryID: e.CategoryID,
		CategoryConfidence: e.Confidence, LineItems: lineItems, RawExtraction: e.Raw,
	})
	if err != nil {
		return fmt.Errorf("receipts: finish: %w", err)
	}
	return nil
}
