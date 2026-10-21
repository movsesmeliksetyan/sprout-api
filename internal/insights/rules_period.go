package insights

import (
	"context"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// peakShare is the part of a month's or a year's spending, in percent, the
// busiest day of the week must take to be worth a card; an even spread
// gives each day about 14.
const peakShare = 20

// peakWeekdayRule names the day of the week the most was spent on.
type peakWeekdayRule struct{}

func (peakWeekdayRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	peak, days := time.Sunday, 0
	for d, amount := range s.Weekdays {
		if amount > 0 {
			days++
		}
		if amount > s.Weekdays[peak] || amount == s.Weekdays[peak] && mondayFirst(time.Weekday(d)) < mondayFirst(peak) {
			peak = time.Weekday(d)
		}
	}
	share, ok := money.Percent(s.Weekdays[peak], s.TotalSpentMinor)
	if !ok || days < 2 || s.Range.Kind != period.Week && share < peakShare {
		return nil
	}
	this, _ := periodNames(s.Range)
	day := peak.String()
	if s.Range.Kind != period.Week {
		day += "s"
	}
	return []Candidate{{
		Kind: KindPeakWeekday, Tone: ToneNeutral, Icon: IconChart,
		Title: "Your biggest day was " + peak.String(),
		Body: fmt.Sprintf("You spent %s on %s %s, %d%% of the total.",
			format(s.Weekdays[peak], s.Currency), day, this, share),
		Score: 50,
	}}
}

// mondayFirst orders the days of the week from Monday, as weeks run.
func mondayFirst(d time.Weekday) int { return (int(d) + 6) % 7 }

// totalTrendRule compares the period's whole spending with the period
// before.
type totalTrendRule struct{}

func (totalTrendRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	pct, ok := money.PercentChange(s.TotalSpentMinor, s.PreviousTotalMinor)
	if !ok {
		return nil
	}
	this, previous := periodNames(s.Range)
	switch {
	case pct <= -trendDownPct && settled(s):
		return []Candidate{{
			Kind: KindTotalSpendDown, Tone: TonePositive, Icon: IconSparkle,
			Title: fmt.Sprintf("You spent %d%% less %s", -pct, this),
			Body: fmt.Sprintf("%s in all, against %s %s.",
				format(s.TotalSpentMinor, s.Currency), format(s.PreviousTotalMinor, s.Currency), previous),
			Score: 62 + min(-pct, 30)/3,
		}}
	case pct >= trendUpPct:
		return []Candidate{{
			Kind: KindTotalSpendUp, Tone: ToneNeutral, Icon: IconChart,
			Title: fmt.Sprintf("You spent %d%% more %s", pct, this),
			Body: fmt.Sprintf("%s in all, against %s %s.",
				format(s.TotalSpentMinor, s.Currency), format(s.PreviousTotalMinor, s.Currency), previous),
			Score: 57 + min(pct, 60)/6,
		}}
	}
	return nil
}

// noData is the card of a period with too little in it.
func noData(s Snapshot) Candidate {
	this, _ := periodNames(s.Range)
	body := "Add a few more expenses " + this + " and your insights will show up here."
	if !s.Range.IsCurrent {
		body = "There were too few expenses " + this + " to say much about it."
	}
	return Candidate{
		Kind: KindNoData, Tone: ToneNeutral, Icon: IconSparkle,
		Title: "Not much to go on yet", Body: body,
	}
}
