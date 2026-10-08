package categories

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

// Budget is the monthly budget to give one category.
type Budget struct {
	CategoryID         uuid.UUID
	MonthlyBudgetMinor int64
}

// UpdateBudgets sets the budgets of several of the user's active categories
// and returns those categories in display order. It is all or nothing: every
// invalid item is reported in an *httpx.ValidationError, keyed
// items.<index>.<field>, and then no budget changes.
func (s *Service) UpdateBudgets(ctx context.Context, userID uuid.UUID, budgets []Budget) ([]db.Category, error) {
	updated := []db.Category{}
	if len(budgets) == 0 {
		return updated, nil
	}

	err := s.write(ctx, userID, func(q *db.Queries) error {
		active, err := q.ListCategories(ctx, db.ListCategoriesParams{UserID: userID})
		if err != nil {
			return fmt.Errorf("categories: list: %w", err)
		}
		isActive := make(map[uuid.UUID]bool, len(active))
		for _, category := range active {
			isActive[category.ID] = true
		}

		fields := map[string]string{}
		ids := make([]uuid.UUID, len(budgets))
		amounts := make([]int64, len(budgets))
		seen := make(map[uuid.UUID]bool, len(budgets))
		for i, budget := range budgets {
			ids[i], amounts[i] = budget.CategoryID, budget.MonthlyBudgetMinor
			switch idField := fmt.Sprintf("items.%d.category_id", i); {
			case !isActive[budget.CategoryID]:
				fields[idField] = "is not one of your active categories"
			case seen[budget.CategoryID]:
				fields[idField] = "is listed more than once"
			}
			seen[budget.CategoryID] = true
			checkBudget(fields, fmt.Sprintf("items.%d.monthly_budget_minor", i), &budget.MonthlyBudgetMinor)
		}
		if len(fields) > 0 {
			return &httpx.ValidationError{Fields: fields}
		}

		err = q.UpdateCategoryBudgets(ctx, db.UpdateCategoryBudgetsParams{UserID: userID, Ids: ids, Amounts: amounts})
		if err != nil {
			return fmt.Errorf("categories: update budgets: %w", err)
		}
		active, err = q.ListCategories(ctx, db.ListCategoriesParams{UserID: userID})
		if err != nil {
			return fmt.Errorf("categories: list: %w", err)
		}
		for _, category := range active {
			if seen[category.ID] {
				updated = append(updated, category)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}
