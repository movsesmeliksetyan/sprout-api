package period

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	require.NoError(t, err)
	return l
}

// d parses a calendar day.
func d(t *testing.T, s string) time.Time {
	t.Helper()
	day, err := time.Parse("2006-01-02", s)
	require.NoError(t, err)
	return day
}

// at parses a wall-clock time in a location.
func at(t *testing.T, s string, l *time.Location) time.Time {
	t.Helper()
	instant, err := time.ParseInLocation("2006-01-02 15:04", s, l)
	require.NoError(t, err)
	return instant
}

func resolve(t *testing.T, kind Kind, offset int, l *time.Location, now time.Time) Range {
	t.Helper()
	r, err := Resolve(kind, offset, l, now)
	require.NoError(t, err)
	return r
}

func days(r Range) string {
	return r.Start.Format("2006-01-02") + ".." + r.End.Format("2006-01-02")
}

func bucketDays(buckets []Bucket) []string {
	out := make([]string, len(buckets))
	for i, b := range buckets {
		out[i] = b.Start.Format("2006-01-02") + ".." + b.End.Format("2006-01-02")
	}
	return out
}

func TestLocalDate(t *testing.T) {
	instant := time.Date(2025, time.July, 20, 23, 30, 0, 0, time.UTC)

	tests := []struct {
		zone string
		want string
	}{
		{"UTC", "2025-07-20"},
		{"Europe/London", "2025-07-21"},       // 00:30 BST
		{"Asia/Yerevan", "2025-07-21"},        // 03:30
		{"America/Los_Angeles", "2025-07-20"}, // 16:30
		{"Pacific/Kiritimati", "2025-07-21"},  // UTC+14
	}

	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			got := LocalDate(instant, loc(t, tt.zone))

			assert.Equal(t, tt.want, got.Format("2006-01-02"))
			assert.Equal(t, time.UTC, got.Location(), "calendar days are held as midnight UTC")
			assert.Equal(t, 0, got.Hour()+got.Minute()+got.Second()+got.Nanosecond())
		})
	}
}

func TestResolve_Week(t *testing.T) {
	london := loc(t, "Europe/London")

	tests := []struct {
		name   string
		now    string
		offset int
		want   string
	}{
		{"first minute of Monday", "2025-07-21 00:00", 0, "2025-07-21..2025-07-27"},
		{"midweek", "2025-07-23 12:00", 0, "2025-07-21..2025-07-27"},
		{"last minute of Sunday", "2025-07-27 23:59", 0, "2025-07-21..2025-07-27"},
		{"next Monday starts a new week", "2025-07-28 00:00", 0, "2025-07-28..2025-08-03"},
		{"previous week", "2025-07-23 12:00", 1, "2025-07-14..2025-07-20"},
		{"four weeks back crosses a month", "2025-07-23 12:00", 4, "2025-06-23..2025-06-29"},
		{"week spanning a year boundary", "2026-01-01 09:00", 0, "2025-12-29..2026-01-04"},
		{"offset across a year boundary", "2026-01-07 09:00", 2, "2025-12-22..2025-12-28"},
		{"week containing a leap day", "2024-02-29 09:00", 0, "2024-02-26..2024-03-03"},
		{"a year of weeks back", "2025-07-23 12:00", 52, "2024-07-22..2024-07-28"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resolve(t, Week, tt.offset, london, at(t, tt.now, london))

			assert.Equal(t, tt.want, days(r))
			assert.Equal(t, time.Monday, r.Start.Weekday())
			assert.Equal(t, time.Sunday, r.End.Weekday())
			assert.Equal(t, tt.offset == 0, r.IsCurrent)
			assert.Equal(t, tt.offset, r.Offset)
			assert.Equal(t, Week, r.Kind)
		})
	}
}

func TestResolve_WeekDependsOnTheUsersTimezone(t *testing.T) {
	// Sunday 23:30 UTC is already Monday in London (BST) and Yerevan, and
	// still Sunday in UTC and Los Angeles.
	instant := time.Date(2025, time.July, 20, 23, 30, 0, 0, time.UTC)

	tests := []struct {
		zone string
		want string
	}{
		{"UTC", "2025-07-14..2025-07-20"},
		{"America/Los_Angeles", "2025-07-14..2025-07-20"},
		{"Europe/London", "2025-07-21..2025-07-27"},
		{"Asia/Yerevan", "2025-07-21..2025-07-27"},
	}

	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			assert.Equal(t, tt.want, days(resolve(t, Week, 0, loc(t, tt.zone), instant)))
		})
	}
}

func TestResolve_WeekAcrossDaylightSavingChanges(t *testing.T) {
	tests := []struct {
		name string
		zone string
		now  string // wall clock in the zone
		want string
	}{
		// Europe/London: clocks go forward Sunday 2025-03-30 at 01:00.
		{"London, hour before clocks go forward", "Europe/London", "2025-03-30 00:30", "2025-03-24..2025-03-30"},
		{"London, hour after clocks go forward", "Europe/London", "2025-03-30 02:30", "2025-03-24..2025-03-30"},
		{"London, Monday after clocks go forward", "Europe/London", "2025-03-31 00:00", "2025-03-31..2025-04-06"},
		// Clocks go back Sunday 2025-10-26 at 02:00; 01:30 happens twice.
		{"London, the repeated hour", "Europe/London", "2025-10-26 01:30", "2025-10-20..2025-10-26"},
		{"London, end of the 25-hour Sunday", "Europe/London", "2025-10-26 23:59", "2025-10-20..2025-10-26"},
		{"London, Monday after clocks go back", "Europe/London", "2025-10-27 00:00", "2025-10-27..2025-11-02"},
		// America/New_York: forward Sunday 2025-03-09, back Sunday 2025-11-02.
		{"New York, spring forward Sunday", "America/New_York", "2025-03-09 03:30", "2025-03-03..2025-03-09"},
		{"New York, fall back Sunday", "America/New_York", "2025-11-02 01:30", "2025-10-27..2025-11-02"},
		{"New York, Monday after fall back", "America/New_York", "2025-11-03 00:00", "2025-11-03..2025-11-09"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zone := loc(t, tt.zone)

			r := resolve(t, Week, 0, zone, at(t, tt.now, zone))

			assert.Equal(t, tt.want, days(r))
			buckets := r.Buckets()
			require.Len(t, buckets, 7, "a week has seven day buckets even when one day is 23 or 25 hours long")
			for i, b := range buckets {
				assert.Equal(t, r.Start.AddDate(0, 0, i), b.Start)
				assert.Equal(t, b.Start, b.End)
			}
		})
	}
}

func TestResolve_UTCInstantAroundADaylightSavingMidnight(t *testing.T) {
	london := loc(t, "Europe/London")
	// 2025-03-30T23:30Z is 00:30 BST on Monday the 31st: the new week has begun.
	instant := time.Date(2025, time.March, 30, 23, 30, 0, 0, time.UTC)

	assert.Equal(t, "2025-03-31..2025-04-06", days(resolve(t, Week, 0, london, instant)))
	// The day before the change the same UTC time was 23:30 GMT on Saturday.
	assert.Equal(t, "2025-03-24..2025-03-30", days(resolve(t, Week, 0, london, instant.AddDate(0, 0, -1))))
}

func TestResolve_Month(t *testing.T) {
	london := loc(t, "Europe/London")

	tests := []struct {
		name        string
		now         string
		offset      int
		want        string
		wantBuckets []string
	}{
		{
			"31 days", "2025-07-21 17:24", 0, "2025-07-01..2025-07-31",
			[]string{"2025-07-01..2025-07-07", "2025-07-08..2025-07-14", "2025-07-15..2025-07-21", "2025-07-22..2025-07-31"},
		},
		{
			"30 days", "2025-04-10 09:00", 0, "2025-04-01..2025-04-30",
			[]string{"2025-04-01..2025-04-07", "2025-04-08..2025-04-14", "2025-04-15..2025-04-21", "2025-04-22..2025-04-30"},
		},
		{
			"28 days", "2025-02-14 09:00", 0, "2025-02-01..2025-02-28",
			[]string{"2025-02-01..2025-02-07", "2025-02-08..2025-02-14", "2025-02-15..2025-02-21", "2025-02-22..2025-02-28"},
		},
		{
			"29 days in a leap year", "2024-02-14 09:00", 0, "2024-02-01..2024-02-29",
			[]string{"2024-02-01..2024-02-07", "2024-02-08..2024-02-14", "2024-02-15..2024-02-21", "2024-02-22..2024-02-29"},
		},
		{"first minute of the month", "2025-07-01 00:00", 0, "2025-07-01..2025-07-31", nil},
		{"last minute of the month", "2025-07-31 23:59", 0, "2025-07-01..2025-07-31", nil},
		{"previous month", "2025-07-21 17:24", 1, "2025-06-01..2025-06-30", nil},
		// A naive "same day last month" would turn 31 March into 3 March.
		{"previous month from the 31st", "2025-03-31 12:00", 1, "2025-02-01..2025-02-28", nil},
		{"previous month from 31 May", "2025-05-31 12:00", 1, "2025-04-01..2025-04-30", nil},
		{"previous month across a year boundary", "2025-01-15 12:00", 1, "2024-12-01..2024-12-31", nil},
		{"twelve months back", "2025-03-31 12:00", 12, "2024-03-01..2024-03-31", nil},
		{"thirteen months back lands on a leap February", "2025-03-31 12:00", 13, "2024-02-01..2024-02-29", nil},
		{"three years back", "2025-07-21 17:24", 36, "2022-07-01..2022-07-31", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resolve(t, Month, tt.offset, london, at(t, tt.now, london))

			assert.Equal(t, tt.want, days(r))
			assert.Equal(t, 1, r.Start.Day())
			assert.Equal(t, tt.offset == 0, r.IsCurrent)
			require.Len(t, r.Buckets(), 4)
			if tt.wantBuckets != nil {
				assert.Equal(t, tt.wantBuckets, bucketDays(r.Buckets()))
			}
		})
	}
}

func TestResolve_MonthDependsOnTheUsersTimezone(t *testing.T) {
	// 22:30 UTC on 31 July is already 1 August in Yerevan.
	instant := time.Date(2025, time.July, 31, 22, 30, 0, 0, time.UTC)

	assert.Equal(t, "2025-07-01..2025-07-31", days(resolve(t, Month, 0, time.UTC, instant)))
	assert.Equal(t, "2025-08-01..2025-08-31", days(resolve(t, Month, 0, loc(t, "Asia/Yerevan"), instant)))
}

func TestResolve_Year(t *testing.T) {
	london := loc(t, "Europe/London")

	tests := []struct {
		name           string
		now            string
		offset         int
		want           string
		wantFebruaryTo string
	}{
		{"current year", "2025-07-21 17:24", 0, "2025-01-01..2025-12-31", "2025-02-28"},
		{"first minute of the year", "2025-01-01 00:00", 0, "2025-01-01..2025-12-31", "2025-02-28"},
		{"last minute of the year", "2025-12-31 23:59", 0, "2025-01-01..2025-12-31", "2025-02-28"},
		{"previous year is a leap year", "2025-07-21 17:24", 1, "2024-01-01..2024-12-31", "2024-02-29"},
		{"two years back", "2025-07-21 17:24", 2, "2023-01-01..2023-12-31", "2023-02-28"},
		{"from a leap day", "2024-02-29 12:00", 1, "2023-01-01..2023-12-31", "2023-02-28"},
		{"century that is not a leap year", "2101-03-01 12:00", 1, "2100-01-01..2100-12-31", "2100-02-28"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resolve(t, Year, tt.offset, london, at(t, tt.now, london))

			assert.Equal(t, tt.want, days(r))
			assert.Equal(t, tt.offset == 0, r.IsCurrent)

			buckets := r.Buckets()
			require.Len(t, buckets, 12)
			for i, b := range buckets {
				assert.Equal(t, time.Month(i+1), b.Start.Month())
				assert.Equal(t, 1, b.Start.Day())
				assert.Equal(t, b.Start.AddDate(0, 1, -1), b.End, "bucket %d ends on the last day of its month", i)
			}
			assert.Equal(t, tt.wantFebruaryTo, buckets[1].End.Format("2006-01-02"))
			assert.Equal(t, r.End, buckets[11].End)
		})
	}
}

func TestResolve_YearDependsOnTheUsersTimezone(t *testing.T) {
	// 22:30 UTC on New Year's Eve is already next year in Yerevan.
	instant := time.Date(2025, time.December, 31, 22, 30, 0, 0, time.UTC)

	assert.Equal(t, "2025-01-01..2025-12-31", days(resolve(t, Year, 0, time.UTC, instant)))
	assert.Equal(t, "2026-01-01..2026-12-31", days(resolve(t, Year, 0, loc(t, "Asia/Yerevan"), instant)))
}

func TestResolve_Errors(t *testing.T) {
	now := time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC)

	_, err := Resolve(Month, -1, time.UTC, now)
	assert.ErrorContains(t, err, "offset must not be negative")

	_, err = Resolve(Kind("decade"), 0, time.UTC, now)
	assert.ErrorContains(t, err, `unknown kind "decade"`)
}

func TestBucketsTileTheRangeExactly(t *testing.T) {
	now := time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC)

	for _, kind := range []Kind{Week, Month, Year} {
		for offset := range 40 {
			r := resolve(t, kind, offset, time.UTC, now)
			buckets := r.Buckets()

			require.Equal(t, r.Start, buckets[0].Start, "%s offset %d", kind, offset)
			require.Equal(t, r.End, buckets[len(buckets)-1].End, "%s offset %d", kind, offset)
			for i := 1; i < len(buckets); i++ {
				require.Equal(t, buckets[i-1].End.AddDate(0, 0, 1), buckets[i].Start,
					"%s offset %d: bucket %d must start the day after bucket %d ends", kind, offset, i, i-1)
			}
		}
	}
}

func TestPreviousMatchesTheNextOffset(t *testing.T) {
	yerevan := loc(t, "Asia/Yerevan")
	nows := []time.Time{
		time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC),
		time.Date(2025, time.March, 31, 12, 0, 0, 0, time.UTC),
		time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, time.December, 31, 22, 30, 0, 0, time.UTC),
	}

	for _, now := range nows {
		for _, kind := range []Kind{Week, Month, Year} {
			for offset := range 30 {
				current := resolve(t, kind, offset, yerevan, now)
				want := resolve(t, kind, offset+1, yerevan, now)

				got := current.Previous()

				require.Equal(t, want, got, "%s offset %d at %s", kind, offset, now)
				require.False(t, got.IsCurrent)
				require.Equal(t, current.Start.AddDate(0, 0, -1), got.End, "periods are contiguous")
			}
		}
	}
}

func TestContains(t *testing.T) {
	r := resolve(t, Month, 0, time.UTC, time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC))

	assert.True(t, r.Contains(d(t, "2025-07-01")))
	assert.True(t, r.Contains(d(t, "2025-07-21")))
	assert.True(t, r.Contains(d(t, "2025-07-31")))
	assert.False(t, r.Contains(d(t, "2025-06-30")))
	assert.False(t, r.Contains(d(t, "2025-08-01")))
}

func TestUnit(t *testing.T) {
	assert.Equal(t, UnitDay, Range{Kind: Week}.Unit())
	assert.Equal(t, UnitWeek, Range{Kind: Month}.Unit())
	assert.Equal(t, UnitMonth, Range{Kind: Year}.Unit())
}

func TestParseKind(t *testing.T) {
	for _, s := range []string{"week", "month", "year"} {
		kind, err := ParseKind(s)
		require.NoError(t, err)
		assert.Equal(t, Kind(s), kind)
	}

	for _, s := range []string{"", "day", "Week", "all"} {
		_, err := ParseKind(s)
		assert.Error(t, err, "%q", s)
	}
}

func TestScaleBudget(t *testing.T) {
	tests := []struct {
		name    string
		monthly int64
		kind    Kind
		want    int64
	}{
		{"month is unchanged", 50000, Month, 50000},
		{"year is twelve months", 50000, Year, 600000},
		{"week is round(monthly x 12 / 52)", 50000, Week, 11538},   // 11538.46
		{"week rounds up", 15000, Week, 3462},                      // 3461.54
		{"week of the design's total budget", 197000, Week, 45462}, // 45461.54
		{"week divides exactly", 1300, Week, 300},
		{"small budget", 100, Week, 23},   // 23.08
		{"one minor unit", 1, Week, 0},    // 0.23
		{"three minor units", 3, Week, 1}, // 0.69
		{"no budget, week", 0, Week, 0},
		{"no budget, month", 0, Month, 0},
		{"no budget, year", 0, Year, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ScaleBudget(tt.monthly, tt.kind))
		})
	}
}
