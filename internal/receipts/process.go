package receipts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// Processor extracts receipts in the background (PRD §6).
type Processor struct {
	db Database
}

// NewProcessor returns a processor working on database.
func NewProcessor(database Database) *Processor {
	return &Processor{db: database}
}

// Process extracts a receipt that is still processing; a receipt deleted,
// confirmed or already read meanwhile is left alone. An error is returned
// only for what a retry may fix.
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
	// Extraction comes with BE-51; until then a receipt cannot be read.
	return Fail(ctx, q, receiptID, Failure{Code: FailureInternal, Message: "Receipt scanning is not available yet."})
}
