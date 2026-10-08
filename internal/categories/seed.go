package categories

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// defaults are the categories every new user starts with (contract §2.3),
// in display order. Their budgets start at zero.
var defaults = []struct {
	name         string
	icon         string
	shade        int16
	categoryType string
}{
	{"Food", "utensils", 0, "food"},
	{"Car", "car", 1, "transport"},
	{"Home", "house", 2, "housing"},
	{"Leisure", "bolt", 3, "leisure"},
	{"Self-care", "sparkles", 4, "selfcare"},
	{"Health", "heart", 5, "health"},
	{"Communal", "droplet", 6, "utilities"},
}

// Seed gives a new user the default categories. It is a users.CreatedHook:
// q writes through the transaction that creates the user. Running it again
// adds nothing.
func Seed(ctx context.Context, q db.Querier, user db.User) error {
	for i, d := range defaults {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("categories: new id: %w", err)
		}
		categoryType := d.categoryType
		err = q.SeedCategory(ctx, db.SeedCategoryParams{
			ID:           id,
			UserID:       user.ID,
			Name:         d.name,
			Icon:         d.icon,
			Shade:        d.shade,
			SortOrder:    int32(i),
			CategoryType: &categoryType,
		})
		if err != nil {
			return fmt.Errorf("categories: seed %q: %w", d.name, err)
		}
	}
	return nil
}
