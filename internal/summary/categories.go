package summary

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// MaxOffset is how many periods back a summary can be asked for: a century
// of months.
const MaxOffset = 1200

// CategoriesOverview is a period's spending against the budgets, category by
// category.
type CategoriesOverview struct {
	Range           period.Range
	TotalSpentMinor int64
	// TotalBudgetMinor is the sum of the items' budgets.
	TotalBudgetMinor int64
	// BudgetUsedPct is nil when there is no budget at all.
	BudgetUsedPct *int
	// Items holds every active category, and an archived one that has spend
	// in the period, largest spend first.
	Items []CategoryItem
}

// CategoryItem is one category's figures for a period.
type CategoryItem struct {
	CategoryID uuid.UUID
	SpentMinor int64
	// BudgetMinor is the monthly budget scaled to the period.
	BudgetMinor int64
	// RemainingMinor is the budget minus the spend; negative when more was
	// spent.
	RemainingMinor int64
	// OverBudget is whether more than the budget was spent. A category
	// without a budget is never over it.
	OverBudget bool
	// BudgetUsedPct is nil when the category has no budget.
	BudgetUsedPct *int
	// SharePct is the part of the period's spending in whole percent; the
	// items of a period add up to 100.
	SharePct int
	TxnCount int
	// TrendPct is the change against the period before; nil when that one
	// had no spend.
	TrendPct *int
}

// Categories returns the user's spending per category in the period of the
// given kind, offset periods back; a nil offset is the current period.
func (s *Service) Categories(ctx context.Context, user db.User, kind string, offset *int) (CategoriesOverview, error) {
	r, err := s.resolveRange(user, kind, offset)
	if err != nil {
		return CategoriesOverview{}, err
	}
	rows, err := db.New(s.db).CategorySpending(ctx, db.CategorySpendingParams{
		UserID:           user.ID,
		FromDate:         r.Start,
		ToDate:           r.End,
		PreviousFromDate: r.Previous().Start,
	})
	if err != nil {
		return CategoriesOverview{}, fmt.Errorf("summary: category spending: %w", err)
	}

	overview := CategoriesOverview{Range: r, Items: make([]CategoryItem, 0, len(rows))}
	amounts := make([]int64, 0, len(rows))
	for _, row := range rows {
		amounts = append(amounts, row.SpentMinor)
	}
	shares := money.Shares(amounts)
	for i, row := range rows {
		budget := period.ScaleBudget(row.MonthlyBudgetMinor, r.Kind)
		overview.Items = append(overview.Items, CategoryItem{
			CategoryID:     row.CategoryID,
			SpentMinor:     row.SpentMinor,
			BudgetMinor:    budget,
			RemainingMinor: budget - row.SpentMinor,
			OverBudget:     budget > 0 && row.SpentMinor > budget,
			BudgetUsedPct:  optional(money.Percent(row.SpentMinor, budget)),
			SharePct:       shares[i],
			TxnCount:       int(row.TxnCount),
			TrendPct:       optional(money.PercentChange(row.SpentMinor, row.PreviousSpentMinor)),
		})
		overview.TotalSpentMinor += row.SpentMinor
		overview.TotalBudgetMinor += budget
	}
	overview.BudgetUsedPct = optional(money.Percent(overview.TotalSpentMinor, overview.TotalBudgetMinor))
	return overview, nil
}

// resolveRange returns the period a summary request names, in the user's
// timezone.
func (s *Service) resolveRange(user db.User, kind string, offset *int) (period.Range, error) {
	parsed, err := period.ParseKind(kind)
	if err != nil {
		return period.Range{}, httpx.WithMessage(httpx.ErrBadRequest, "The period must be week, month or year.")
	}
	back := 0
	if offset != nil {
		back = *offset
	}
	if back < 0 || back > MaxOffset {
		return period.Range{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The offset must be between 0 and %d.", MaxOffset))
	}
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return period.Range{}, fmt.Errorf("summary: timezone %q: %w", user.Timezone, err)
	}
	r, err := period.Resolve(parsed, back, loc, s.now())
	if err != nil {
		return period.Range{}, fmt.Errorf("summary: resolve period: %w", err)
	}
	return r, nil
}

// optional turns a percentage that may not exist into the pointer the
// contract's nullable fields need.
func optional(pct int, ok bool) *int {
	if !ok {
		return nil
	}
	return &pct
}
