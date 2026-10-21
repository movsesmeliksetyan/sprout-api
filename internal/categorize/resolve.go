package categorize

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// resolver turns a category type into one of the user's categories (PRD
// §5.2, "Type → category resolution").
type resolver map[string]uuid.UUID

// newResolver reads the user's active categories in one query. A type with
// one category resolves to it; with several, to the one holding the most
// transactions, the earlier in display order on a tie. A type the user has
// no active category of does not resolve.
func newResolver(ctx context.Context, q db.Querier, userID uuid.UUID) (resolver, error) {
	rows, err := q.ListTypedCategories(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("categorize: categories: %w", err)
	}
	r := make(resolver, len(rows))
	most := make(map[string]int64, len(rows))
	for _, row := range rows {
		if _, ok := r[row.CategoryType]; ok && row.Transactions <= most[row.CategoryType] {
			continue
		}
		r[row.CategoryType] = row.ID
		most[row.CategoryType] = row.Transactions
	}
	return r, nil
}

// resolve returns the user's category of a type, and whether there is one.
func (r resolver) resolve(categoryType string) (uuid.UUID, bool) {
	id, ok := r[categoryType]
	return id, ok
}
