package imports

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// Process runs the statement pipeline for an import, in the background
// job. It starts from scratch every time — what an earlier attempt wrote is
// wiped — so a retried job rebuilds the same rows. A committed import is
// left alone.
func Process(ctx context.Context, database Database, importID uuid.UUID) error {
	return db.WithTx(ctx, database, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetImportByID(ctx, importID)
		if err != nil {
			return fmt.Errorf("imports: process: %w", err)
		}
		if Status(found.Status) == StatusCommitted {
			return nil
		}
		if err := q.StartImportProcessing(ctx, importID); err != nil {
			return fmt.Errorf("imports: process: %w", err)
		}
		if err := q.DeleteImportRows(ctx, importID); err != nil {
			return fmt.Errorf("imports: process: %w", err)
		}
		// The pipeline's stages arrive with BE-38 to BE-47.
		return fail(ctx, q, importID, Failure{Code: FailureInternal, Message: "Statements cannot be processed yet."})
	})
}

// Fail marks an import failed, as the job does once its attempts run out.
func Fail(ctx context.Context, q db.Querier, importID uuid.UUID, failure Failure) error {
	return fail(ctx, q, importID, failure)
}

func fail(ctx context.Context, q db.Querier, importID uuid.UUID, failure Failure) error {
	encoded, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("imports: failure: %w", err)
	}
	if err := q.FailImport(ctx, db.FailImportParams{ID: importID, Failure: encoded}); err != nil {
		return fmt.Errorf("imports: fail: %w", err)
	}
	return nil
}
