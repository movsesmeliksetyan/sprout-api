package summary

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// breakdownTop is how many categories a breakdown names before the rest
// become one row.
const breakdownTop = 5

// Stats is a period's total spending, over time and by category.
type Stats struct {
	Range           period.Range
	TotalSpentMinor int64
	// PreviousTotalMinor is the spending of the whole period before.
	PreviousTotalMinor int64
	// DeltaMinor is this period minus the previous one.
	DeltaMinor int64
	// DeltaPct is nil when the previous period had no spend.
	DeltaPct *int
	Series   Series
	// Breakdown holds the categories with spend, largest first: the top
	// five, then one row for all the others when there are more.
	Breakdown []BreakdownRow
}

// Series is a period's total spending across its buckets.
type Series struct {
	Unit period.Unit
	// Buckets covers the whole period; one without spend, or still to come,
	// holds 0.
	Buckets []BucketAmount
	// PeakIndex is the position of the highest bucket: the first one on a
	// tie, and 0 when nothing was spent.
	PeakIndex int
}

// BreakdownRow is one slice of a period's spending.
type BreakdownRow struct {
	// CategoryID is nil for the row that adds up the categories outside the
	// top five.
	CategoryID  *uuid.UUID
	AmountMinor int64
	// SharePct is the part of the spending in whole percent; the rows of a
	// breakdown add up to 100.
	SharePct int
}

// Stats returns the user's total spending in the period of the given kind,
// offset periods back; a nil offset is the current period.
func (s *Service) Stats(ctx context.Context, user db.User, kind string, offset *int) (Stats, error) {
	r, _, err := s.resolveRange(user, kind, offset)
	if err != nil {
		return Stats{}, err
	}
	q := db.New(s.db)

	byCategory, err := q.SpentByCategory(ctx, db.SpentByCategoryParams{UserID: user.ID, FromDate: r.Start, ToDate: r.End})
	if err != nil {
		return Stats{}, fmt.Errorf("summary: spent by category: %w", err)
	}
	previous := r.Previous()
	previousTotal, err := q.SpentBetween(ctx, db.SpentBetweenParams{UserID: user.ID, FromDate: previous.Start, ToDate: previous.End})
	if err != nil {
		return Stats{}, fmt.Errorf("summary: previous spent: %w", err)
	}
	byDay, err := q.SpentByDay(ctx, db.SpentByDayParams{UserID: user.ID, FromDate: r.Start, ToDate: r.End})
	if err != nil {
		return Stats{}, fmt.Errorf("summary: spent by day: %w", err)
	}

	var total int64
	for _, row := range byCategory {
		total += row.SpentMinor
	}
	days := make([]dayAmount, 0, len(byDay))
	for _, row := range byDay {
		days = append(days, dayAmount{Date: row.LocalDate, AmountMinor: row.SpentMinor})
	}
	buckets := fillBuckets(r, days)
	_, peakIndex := peakOf(buckets)

	return Stats{
		Range:              r,
		TotalSpentMinor:    total,
		PreviousTotalMinor: previousTotal,
		DeltaMinor:         total - previousTotal,
		DeltaPct:           optional(money.PercentChange(total, previousTotal)),
		Series:             Series{Unit: r.Unit(), Buckets: buckets, PeakIndex: peakIndex},
		Breakdown:          breakdown(byCategory),
	}, nil
}

// breakdown turns the spending per category, largest first, into the rows
// of a breakdown.
func breakdown(byCategory []db.SpentByCategoryRow) []BreakdownRow {
	rows := make([]BreakdownRow, 0, min(len(byCategory), breakdownTop+1))
	for i, row := range byCategory {
		if i < breakdownTop {
			rows = append(rows, BreakdownRow{CategoryID: &row.CategoryID, AmountMinor: row.SpentMinor})
			continue
		}
		if i == breakdownTop {
			rows = append(rows, BreakdownRow{})
		}
		rows[breakdownTop].AmountMinor += row.SpentMinor
	}

	amounts := make([]int64, 0, len(rows))
	for _, row := range rows {
		amounts = append(amounts, row.AmountMinor)
	}
	for i, share := range money.Shares(amounts) {
		rows[i].SharePct = share
	}
	return rows
}
