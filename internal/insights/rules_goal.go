package insights

import (
	"context"
	"fmt"

	"github.com/movsesmeliksetyan/sprout-api/internal/money"
)

// almostTherePct is how much of a goal is saved before it is almost there.
const almostTherePct = 80

// goalRule cheers on the goals that are nearly saved, or on course at the
// pace the user keeps.
type goalRule struct{}

func (goalRule) Evaluate(_ context.Context, s Snapshot) []Candidate {
	var out []Candidate
	for _, g := range s.Goals {
		remaining := g.TargetMinor - g.SavedMinor
		if remaining <= 0 {
			continue
		}
		pct, _ := money.Percent(g.SavedMinor, g.TargetMinor)
		id := g.ID
		if pct >= almostTherePct {
			out = append(out, Candidate{
				Kind: KindGoalAlmostThere, Tone: TonePositive, Icon: IconTarget, GoalID: &id,
				Title: "Almost there with " + g.Title,
				Body: fmt.Sprintf("Just %s to go: you've saved %d%% of %s.",
					format(remaining, s.Currency), min(pct, 99), format(g.TargetMinor, s.Currency)),
				Score: 75,
			})
		}
		if g.MonthlyPaceMinor > 0 && g.EtaMonth != nil {
			out = append(out, Candidate{
				Kind: KindGoalOnTrack, Tone: TonePositive, Icon: IconTarget, GoalID: &id,
				Title: sentenceStart(g.Title) + " is on track",
				Body: fmt.Sprintf("At %s a month you'll reach %s by %s.",
					format(g.MonthlyPaceMinor, s.Currency), format(g.TargetMinor, s.Currency), etaName(*g.EtaMonth)),
				Score: 45,
			})
		}
	}
	return out
}
