package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// CategoryOption changes the category NewCategory creates.
type CategoryOption func(*db.CreateCategoryParams)

// CategoryName sets the name in place of a generated one.
func CategoryName(name string) CategoryOption {
	return func(p *db.CreateCategoryParams) { p.Name = name }
}

// CategoryBudget sets the monthly budget, which is zero otherwise.
func CategoryBudget(minor int64) CategoryOption {
	return func(p *db.CreateCategoryParams) { p.MonthlyBudgetMinor = minor }
}

// CategoryType sets the canonical type, which is none otherwise.
func CategoryType(key string) CategoryOption {
	return func(p *db.CreateCategoryParams) { p.CategoryType = &key }
}

// CategoryArchived creates the category already archived.
func CategoryArchived() CategoryOption {
	return func(p *db.CreateCategoryParams) {
		now := time.Now()
		p.ArchivedAt = &now
	}
}

// NewCategory stores a category for the user, after the ones they have. It
// has a name of its own unless CategoryName says otherwise.
func NewCategory(t testing.TB, database db.DBTX, userID uuid.UUID, opts ...CategoryOption) db.Category {
	t.Helper()
	ctx := context.Background()
	q := db.New(database)

	id, err := uuid.NewV7()
	require.NoError(t, err)
	sortOrder, err := q.NextCategorySortOrder(ctx, userID)
	require.NoError(t, err)
	params := db.CreateCategoryParams{
		ID:        id,
		UserID:    userID,
		Name:      "Category " + id.String()[24:],
		Icon:      "tag",
		SortOrder: sortOrder,
	}
	for _, opt := range opts {
		opt(&params)
	}

	category, err := q.CreateCategory(ctx, params)
	require.NoError(t, err)
	return category
}
