package imports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

// CommitResult is what a commit wrote: a transaction for each included
// expense and income row; the other rows are skipped.
type CommitResult struct {
	Created int
	Skipped int
}

// Commit writes a ready import's included rows to the user's ledger, in one
// transaction: each becomes a transaction (source import, with the row's
// id), the expenses' merchants vote for their categories' types (§5.3),
// and the import is committed and read-only, its statement file deleted
// after ClosedFileTTL. Committing again changes nothing and returns the
// same result. Included expenses without a category are a
// httpx.ValidationError keyed by row id; an import still processing,
// needing a mapping or failed is httpx.ErrConflict.
func (s *Service) Commit(ctx context.Context, userID, id uuid.UUID) (CommitResult, error) {
	var result CommitResult
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		found, err := q.GetImportForUpdate(ctx, db.GetImportForUpdateParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("imports: commit: %w", err)
		}
		switch Status(found.Status) {
		case StatusCommitted:
			result, err = committed(ctx, q, id)
			return err
		case StatusReady:
		default:
			return httpx.WithMessage(httpx.ErrConflict, "The import is not ready to commit.")
		}

		missing, err := q.ListIncompleteExpenseRows(ctx, id)
		if err != nil {
			return fmt.Errorf("imports: commit: %w", err)
		}
		if len(missing) > 0 {
			fields := make(map[string]string, len(missing))
			for _, row := range missing {
				fields[row.String()] = "needs a category"
			}
			return &httpx.ValidationError{Message: "Choose a category for every expense you import.", Fields: fields}
		}

		now := time.Now()
		if _, err := q.CommitImportRows(ctx, id); err != nil {
			return fmt.Errorf("imports: commit rows: %w", err)
		}
		if err := vote(ctx, q, userID, id, now); err != nil {
			return err
		}
		if err := q.MarkImportCommitted(ctx, db.MarkImportCommittedParams{ID: id, CommittedAt: &now}); err != nil {
			return fmt.Errorf("imports: commit: %w", err)
		}
		if err := keepFile(ctx, q, found.UploadID, ClosedFileTTL); err != nil {
			return err
		}
		result, err = committed(ctx, q, id)
		return err
	})
	if err != nil {
		return CommitResult{}, err
	}
	return result, nil
}

// committed is what the commit of an import wrote, read from its rows,
// which do not change after it.
func committed(ctx context.Context, q db.Querier, id uuid.UUID) (CommitResult, error) {
	counts, err := q.CountCommittedRows(ctx, id)
	if err != nil {
		return CommitResult{}, fmt.Errorf("imports: commit: %w", err)
	}
	return CommitResult{Created: int(counts.CreatedCount), Skipped: int(counts.RowCount - counts.CreatedCount)}, nil
}

// vote records the user's votes from the committed expenses.
func vote(ctx context.Context, q db.Querier, userID, id uuid.UUID, at time.Time) error {
	rows, err := q.ListCommittedVotes(ctx, id)
	if err != nil {
		return fmt.Errorf("imports: votes: %w", err)
	}
	votes := make([]categorize.Vote, len(rows))
	for i, row := range rows {
		votes[i] = categorize.Vote{MerchantKey: row.MerchantKey, DisplayName: row.DisplayName, CategoryID: row.CategoryID}
	}
	return categorize.RecordVotes(ctx, q, userID, votes, at)
}
