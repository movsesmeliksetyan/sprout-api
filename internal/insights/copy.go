package insights

import (
	"fmt"
	"strings"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// The cards' words: warm, second person, sentence case, English only
// (contract §2.7). Every amount goes through money.Format.

// periodNames are how a card names its period and the one before it:
// "this month" and "last month" for the current period, "in July 2025" and
// "the month before" for a past one.
func periodNames(r period.Range) (this, previous string) {
	unit := map[period.Kind]string{period.Week: "week", period.Month: "month", period.Year: "year"}[r.Kind]
	switch {
	case r.IsCurrent:
		return "this " + unit, "last " + unit
	case r.Offset == 1:
		return "last " + unit, "the " + unit + " before"
	}
	switch r.Kind {
	case period.Week:
		return "the week of " + r.Start.Format("2 January"), "the week before"
	case period.Month:
		return "in " + r.Start.Format("January 2006"), "the month before"
	default:
		return "in " + r.Start.Format("2006"), "the year before"
	}
}

// ordinal is a day of the month as "the 12th".
func ordinal(day int) string {
	suffix := "th"
	if day%100 < 11 || day%100 > 13 {
		switch day % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("the %d%s", day, suffix)
}

// expenseDay names the day of an expense: "on the 12th" within a week or
// month, "on 12 March" within a year.
func expenseDay(r period.Range, d time.Time) string {
	if r.Kind == period.Year {
		return "on " + d.Format("2 January")
	}
	return "on " + ordinal(d.Day())
}

// sentenceStart capitalises the first letter of a name used to open a
// title, leaving the rest as the user wrote it.
func sentenceStart(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// plural writes n with a noun, "1 day" or "9 days".
func plural(n int64, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func format(minor int64, currency string) string { return money.Format(minor, currency) }

// etaName is a YYYY-MM month as "March 2026".
func etaName(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.Format("January 2006")
}
