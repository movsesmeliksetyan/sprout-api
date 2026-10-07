// Package period resolves week, month and year ranges and buckets in a user's timezone.
package period

import (
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/money"
)

// Kind is the length of a summary period.
type Kind string

// The summary periods of the API contract §1.2.
const (
	Week  Kind = "week"
	Month Kind = "month"
	Year  Kind = "year"
)

// Unit is what one bucket of a period's series covers.
type Unit string

// Bucket units: a week is split into days, a month into weeks, a year into months.
const (
	UnitDay   Unit = "day"
	UnitWeek  Unit = "week"
	UnitMonth Unit = "month"
)

// ParseKind converts the wire value of a period.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case Week, Month, Year:
		return Kind(s), nil
	}
	return "", fmt.Errorf("period: unknown kind %q", s)
}

// LocalDate returns the calendar day of instant t in loc. Calendar days are
// held as midnight UTC throughout, matching how a DATE column is read, so
// arithmetic on them is never affected by daylight saving.
func LocalDate(t time.Time, loc *time.Location) time.Time {
	year, month, day := t.In(loc).Date()
	return date(year, month, day)
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// Range is one period: calendar days Start to End, both inclusive.
type Range struct {
	Kind      Kind
	Offset    int
	Start     time.Time
	End       time.Time
	IsCurrent bool
}

// Bucket is one slice of a Range: calendar days Start to End, both inclusive.
type Bucket struct {
	Start time.Time
	End   time.Time
}

// Resolve returns the period of the given kind that lies offset periods
// before the one containing now, as seen in loc. Offset 0 is the current
// period. Weeks run Monday to Sunday.
func Resolve(kind Kind, offset int, loc *time.Location, now time.Time) (Range, error) {
	if offset < 0 {
		return Range{}, fmt.Errorf("period: offset must not be negative (got %d)", offset)
	}
	today := LocalDate(now, loc)
	year, month, day := today.Date()

	r := Range{Kind: kind, Offset: offset, IsCurrent: offset == 0}
	switch kind {
	case Week:
		sinceMonday := (int(today.Weekday()) + 6) % 7
		r.Start = date(year, month, day-sinceMonday-7*offset)
		r.End = r.Start.AddDate(0, 0, 6)
	case Month:
		r.Start = date(year, month-time.Month(offset), 1)
		r.End = r.Start.AddDate(0, 1, -1)
	case Year:
		r.Start = date(year-offset, time.January, 1)
		r.End = date(year-offset, time.December, 31)
	default:
		return Range{}, fmt.Errorf("period: unknown kind %q", kind)
	}
	return r, nil
}

// Previous returns the period immediately before r, the one trends are
// measured against.
func (r Range) Previous() Range {
	prev := Range{Kind: r.Kind, Offset: r.Offset + 1}
	switch r.Kind {
	case Week:
		prev.Start = r.Start.AddDate(0, 0, -7)
		prev.End = r.End.AddDate(0, 0, -7)
	case Month:
		prev.Start = r.Start.AddDate(0, -1, 0)
		prev.End = r.Start.AddDate(0, 0, -1)
	case Year:
		prev.Start = r.Start.AddDate(-1, 0, 0)
		prev.End = r.Start.AddDate(0, 0, -1)
	}
	return prev
}

// Contains reports whether calendar day d falls in r.
func (r Range) Contains(d time.Time) bool {
	return !d.Before(r.Start) && !d.After(r.End)
}

// Unit returns what each of r's buckets covers.
func (r Range) Unit() Unit {
	switch r.Kind {
	case Week:
		return UnitDay
	case Month:
		return UnitWeek
	default:
		return UnitMonth
	}
}

// Buckets splits r for trend and series charts: a week into its 7 days, a
// month into days 1–7, 8–14, 15–21 and 22–end, a year into its 12 months.
func (r Range) Buckets() []Bucket {
	switch r.Kind {
	case Week:
		buckets := make([]Bucket, 7)
		for i := range buckets {
			day := r.Start.AddDate(0, 0, i)
			buckets[i] = Bucket{Start: day, End: day}
		}
		return buckets
	case Month:
		return []Bucket{
			{Start: r.Start, End: r.Start.AddDate(0, 0, 6)},
			{Start: r.Start.AddDate(0, 0, 7), End: r.Start.AddDate(0, 0, 13)},
			{Start: r.Start.AddDate(0, 0, 14), End: r.Start.AddDate(0, 0, 20)},
			{Start: r.Start.AddDate(0, 0, 21), End: r.End},
		}
	case Year:
		buckets := make([]Bucket, 12)
		for i := range buckets {
			start := r.Start.AddDate(0, i, 0)
			buckets[i] = Bucket{Start: start, End: start.AddDate(0, 1, -1)}
		}
		return buckets
	}
	return nil
}

// ScaleBudget converts a category's monthly budget to the budget for one
// period of the given kind: unchanged for a month, ×12 for a year, and
// round(monthly × 12 / 52) for a week.
func ScaleBudget(monthlyMinor int64, kind Kind) int64 {
	switch kind {
	case Week:
		return money.DivRound(monthlyMinor*12, 52)
	case Year:
		return monthlyMinor * 12
	default:
		return monthlyMinor
	}
}
