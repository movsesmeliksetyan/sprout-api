package summary

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// CategoryDetail is one category's spending in a period, against its budget
// and bucket by bucket.
type CategoryDetail struct {
	Range      period.Range
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
	TxnCount      int
	Trend         Trend
}

// Trend is a period's spending across its buckets.
type Trend struct {
	Unit period.Unit
	// Buckets covers the whole period; one without spend, or still to come,
	// holds 0.
	Buckets []BucketAmount
	// AverageMinor is the spend per bucket, counting only the buckets that
	// have started when the period is the current one.
	AverageMinor int64
	// PeakMinor is the amount of the highest bucket and PeakIndex its
	// position: the first one on a tie, and 0 when nothing was spent.
	PeakMinor int64
	PeakIndex int
}

// BucketAmount is what was spent on the calendar days Start to End, both
// included.
type BucketAmount struct {
	Start       time.Time
	End         time.Time
	AmountMinor int64
}

// dayAmount is what was spent on one calendar day.
type dayAmount struct {
	Date        time.Time
	AmountMinor int64
}

// Category returns the user's spending in one category in the period of the
// given kind, offset periods back; a nil offset is the current period. An
// archived category can still be read.
func (s *Service) Category(ctx context.Context, user db.User, categoryID uuid.UUID, kind string, offset *int) (CategoryDetail, error) {
	r, today, err := s.resolveRange(user, kind, offset)
	if err != nil {
		return CategoryDetail{}, err
	}
	q := db.New(s.db)
	category, err := q.GetCategory(ctx, db.GetCategoryParams{ID: categoryID, UserID: user.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return CategoryDetail{}, httpx.ErrNotFound
	}
	if err != nil {
		return CategoryDetail{}, fmt.Errorf("summary: get category: %w", err)
	}
	rows, err := q.CategorySpentByDay(ctx, db.CategorySpentByDayParams{
		UserID:     user.ID,
		CategoryID: category.ID,
		FromDate:   r.Start,
		ToDate:     r.End,
	})
	if err != nil {
		return CategoryDetail{}, fmt.Errorf("summary: category spent by day: %w", err)
	}

	var spent int64
	var count int
	days := make([]dayAmount, 0, len(rows))
	for _, row := range rows {
		spent += row.SpentMinor
		count += int(row.TxnCount)
		days = append(days, dayAmount{Date: row.LocalDate, AmountMinor: row.SpentMinor})
	}
	budget := budgetFor(spent, category.MonthlyBudgetMinor, r.Kind)
	return CategoryDetail{
		Range:          r,
		CategoryID:     category.ID,
		SpentMinor:     spent,
		BudgetMinor:    budget.BudgetMinor,
		RemainingMinor: budget.RemainingMinor,
		OverBudget:     budget.Over,
		BudgetUsedPct:  budget.UsedPct,
		TxnCount:       count,
		Trend:          newTrend(r, today, days),
	}, nil
}

// newTrend spreads the spending of days, which lie in r in date order, over
// r's buckets. today decides which buckets of the current period have
// started.
func newTrend(r period.Range, today time.Time, days []dayAmount) Trend {
	buckets := fillBuckets(r, days)
	trend := Trend{Unit: r.Unit(), Buckets: buckets}

	var total int64
	started := 0
	for i, bucket := range buckets {
		total += bucket.AmountMinor
		if bucket.AmountMinor > trend.PeakMinor {
			trend.PeakMinor, trend.PeakIndex = bucket.AmountMinor, i
		}
		if !r.IsCurrent || !bucket.Start.After(today) {
			started++
		}
	}
	if started > 0 {
		trend.AverageMinor = money.DivRound(total, int64(started))
	}
	return trend
}

// fillBuckets returns r's buckets with the amounts of days, which lie in r
// in date order, added to the bucket each falls in.
func fillBuckets(r period.Range, days []dayAmount) []BucketAmount {
	ranges := r.Buckets()
	buckets := make([]BucketAmount, len(ranges))
	for i, b := range ranges {
		buckets[i] = BucketAmount{Start: b.Start, End: b.End}
	}
	i := 0
	for _, day := range days {
		for i < len(buckets)-1 && day.Date.After(buckets[i].End) {
			i++
		}
		buckets[i].AmountMinor += day.AmountMinor
	}
	return buckets
}
