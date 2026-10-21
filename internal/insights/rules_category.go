package insights

import (
	"context"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/money"
)

const (
	// trendDownPct and trendUpPct are the changes against the period
	// before that make a category or the total worth a card.
	trendDownPct = 5
	trendUpPct   = 20
	// materialShare is the part of the period's spending, in percent, a
	// category's change must reach to be worth a card: a coffee costing
	// half what it did last month is not news.
	materialShare = 2
	// nearBudgetPct is how much of a budget is used before a card warns
	// that it is close.
	nearBudgetPct = 85
)

// categoryTrendRule finds the categories spent on noticeably less, or
// more, than in the period before.
type categoryTrendRule struct{}

func (categoryTrendRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	this, previous := periodNames(s.Range)
	var out []Candidate
	for _, c := range s.Categories {
		pct, ok := money.PercentChange(c.SpentMinor, c.PreviousSpentMinor)
		diff := c.SpentMinor - c.PreviousSpentMinor
		if !ok || c.SpentMinor == 0 || abs(diff)*100 < s.TotalSpentMinor*materialShare {
			continue
		}
		id := c.ID
		switch {
		case pct <= -trendDownPct && settled(s):
			out = append(out, Candidate{
				Kind: KindCategorySpendDown, Tone: TonePositive, Icon: IconSparkle, CategoryID: &id,
				Title: fmt.Sprintf("You spent %d%% less on %s", -pct, c.Name),
				Body: fmt.Sprintf("%s %s, down from %s %s. Nice work.",
					format(c.SpentMinor, s.Currency), this, format(c.PreviousSpentMinor, s.Currency), previous),
				Score: 60 + min(-pct, 30)/3,
			})
		case pct >= trendUpPct:
			out = append(out, Candidate{
				Kind: KindCategorySpendUp, Tone: ToneNeutral, Icon: IconChart, CategoryID: &id,
				Title: fmt.Sprintf("You spent %d%% more on %s", pct, c.Name),
				Body: fmt.Sprintf("%s %s, up from %s %s.",
					format(c.SpentMinor, s.Currency), this, format(c.PreviousSpentMinor, s.Currency), previous),
				Score: 55 + min(pct, 60)/6,
			})
		}
	}
	return out
}

// overBudgetRule finds the categories that went past their budget, naming
// the expense that did most of it.
type overBudgetRule struct{}

func (overBudgetRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	var out []Candidate
	for _, c := range s.Categories {
		if c.BudgetMinor <= 0 || c.SpentMinor <= c.BudgetMinor {
			continue
		}
		over := c.SpentMinor - c.BudgetMinor
		body := fmt.Sprintf("%s above your %s limit.", format(over, s.Currency), format(c.BudgetMinor, s.Currency))
		if l := c.Largest; l != nil && l.AmountMinor*2 > c.SpentMinor {
			body += fmt.Sprintf(" Most of it was one %s expense", format(l.AmountMinor, s.Currency))
			if l.Merchant != nil && *l.Merchant != "" {
				body += " at " + *l.Merchant
			}
			body += " " + expenseDay(s.Range, l.LocalDate) + "."
		}
		overPct, _ := money.Percent(over, c.BudgetMinor)
		id := c.ID
		out = append(out, Candidate{
			Kind: KindCategoryOverBudget, Tone: ToneWarning, Icon: IconWarning, CategoryID: &id,
			Title: sentenceStart(c.Name) + " is over budget",
			Body:  body,
			Score: 90 + min(overPct, 50)/5,
		})
	}
	return out
}

// nearBudgetRule warns, while the period is still running, about the
// categories close to their budget.
type nearBudgetRule struct{}

func (nearBudgetRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	if !s.Range.IsCurrent {
		return nil
	}
	daysLeft := daysBetween(s.Today, s.Range.End)
	var out []Candidate
	for _, c := range s.Categories {
		used, ok := money.Percent(c.SpentMinor, c.BudgetMinor)
		if !ok || c.BudgetMinor < 0 || used < nearBudgetPct || c.SpentMinor > c.BudgetMinor {
			continue
		}
		body := fmt.Sprintf("%s left of your %s limit", format(c.BudgetMinor-c.SpentMinor, s.Currency), format(c.BudgetMinor, s.Currency))
		if daysLeft > 0 {
			body += ", with " + plural(daysLeft, "day") + " to go"
		}
		id := c.ID
		out = append(out, Candidate{
			Kind: KindCategoryNearBudget, Tone: ToneWarning, Icon: IconWarning, CategoryID: &id,
			Title: sentenceStart(c.Name) + " is close to its budget",
			Body:  body + ".",
			Score: 70 + (used - nearBudgetPct),
		})
	}
	return out
}

// settled reports whether a period is far enough along for "less than the
// period before" to mean something: any past period, or a current one at
// least half over. Early in a month everything is less than last month.
func settled(s Snapshot) bool {
	if !s.Range.IsCurrent {
		return true
	}
	total := daysBetween(s.Range.Start, s.Range.End) + 1
	elapsed := daysBetween(s.Range.Start, s.Today) + 1
	return elapsed*2 >= total
}

// daysBetween counts the calendar days from a to b.
func daysBetween(a, b time.Time) int64 {
	return int64(b.Sub(a).Round(time.Hour).Hours()) / 24
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
