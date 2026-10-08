package goals

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func TestPaceAndETA(t *testing.T) {
	t.Parallel()
	today := day(2025, time.July, 21)
	old := today.AddDate(-1, 0, 0)
	eta := func(month string) *string { return &month }

	tests := []struct {
		name string
		// netMinor is what the goal gained in the trailing 90 days.
		netMinor       int64
		created        time.Time
		remainingMinor int64
		today          time.Time
		wantPace       int64
		wantETA        *string
	}{
		{"new goal, nothing saved", 0, today, 12000, today, 0, nil},
		{"new goal, first top-up today", 2500, today, 9500, today, 2500, eta("2025-11")},
		{"steady saver", 7500, old, 5200, today, 2500, eta("2025-10")},
		{"steady saver, a whole number of months left", 7500, old, 5000, today, 2500, eta("2025-09")},
		{"stalled goal: nothing in the window", 0, old, 5200, today, 0, nil},
		{"completed goal", 7500, old, 0, today, 2500, nil},
		{"withdrawal-heavy goal", -3000, old, 9000, today, -1000, nil},
		{"withdrawals equal to top-ups", 0, old, 9000, today, 0, nil},
		{"a goal 45 days old averages over its 45 days", 3000, today.AddDate(0, 0, -45), 4000, today, 2000, eta("2025-09")},
		{"a goal 10 days old counts as a month old", 1000, today.AddDate(0, 0, -10), 1000, today, 1000, eta("2025-08")},
		{"a goal exactly 90 days old", 9000, today.AddDate(0, 0, -90), 100, today, 3000, eta("2025-08")},
		{"the pace is rounded once", 1000, old, 500, today, 333, eta("2025-09")},
		{"into the next year", 3000, old, 3000, day(2025, time.November, 30), 1000, eta("2026-02")},
		{"December", 3000, old, 1000, day(2025, time.November, 30), 1000, eta("2025-12")},
		{"a hundred years away, to the month", 3, old, 1200, today, 1, eta("2125-07")},
		{"further away than that", 3, old, 1201, today, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pace := monthlyPace(tt.netMinor, tt.created, tt.today)
			assert.Equal(t, tt.wantPace, pace)
			assert.Equal(t, tt.wantETA, etaMonth(tt.remainingMinor, pace, tt.today))
		})
	}
}
