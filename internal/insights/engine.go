package insights

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// Kind is what an insight is about (contract §2.7).
type Kind string

// The kinds of insight.
const (
	KindCategorySpendDown  Kind = "category_spend_down"
	KindCategorySpendUp    Kind = "category_spend_up"
	KindCategoryOverBudget Kind = "category_over_budget"
	KindCategoryNearBudget Kind = "category_near_budget"
	KindPeakWeekday        Kind = "peak_weekday"
	KindTotalSpendDown     Kind = "total_spend_down"
	KindTotalSpendUp       Kind = "total_spend_up"
	KindGoalOnTrack        Kind = "goal_on_track"
	KindGoalAlmostThere    Kind = "goal_almost_there"
	KindNoData             Kind = "no_data"
)

// Tone is how a card reads: good news, a caution, or plain information.
type Tone string

// The tones.
const (
	TonePositive Tone = "positive"
	ToneWarning  Tone = "warning"
	ToneNeutral  Tone = "neutral"
)

// Icon is the card's symbol.
type Icon string

// The icons.
const (
	IconSparkle Icon = "sparkle"
	IconWarning Icon = "warning"
	IconChart   Icon = "chart"
	IconTarget  Icon = "target"
)

// MaxCards is how many insights a period shows at most.
const MaxCards = 5

// MinTransactions is how many expenses a period needs before it has
// anything to say; below it the only card is no_data.
const MinTransactions = 3

// Snapshot is what the rules look at: a period's spending set against the
// period before, its days of the week, and the user's goals. Amounts are
// minor units of Currency.
type Snapshot struct {
	Range period.Range
	// Today is the user's calendar day; it says how much of a current
	// period is left.
	Today    time.Time
	Currency string
	// TxnCount is the number of expenses in the period.
	TxnCount           int
	TotalSpentMinor    int64
	PreviousTotalMinor int64
	Categories         []Category
	// Weekdays is the period's spending per day of the week, indexed by
	// time.Weekday.
	Weekdays [7]int64
	Goals    []Goal
}

// Category is one category's figures for the period.
type Category struct {
	ID                 uuid.UUID
	Name               string
	SpentMinor         int64
	PreviousSpentMinor int64
	// BudgetMinor is the monthly budget scaled to the period; 0 is none.
	BudgetMinor int64
	TxnCount    int
	// Largest is the category's largest expense in the period; nil when
	// it has none.
	Largest *Expense
}

// Expense is one transaction a card can point at.
type Expense struct {
	AmountMinor int64
	Merchant    *string
	LocalDate   time.Time
}

// Goal is an active goal with its pace.
type Goal struct {
	ID          uuid.UUID
	Title       string
	TargetMinor int64
	SavedMinor  int64
	// MonthlyPaceMinor is what the goal gained per month lately.
	MonthlyPaceMinor int64
	// EtaMonth is the month, YYYY-MM, the goal is saved in at its pace.
	EtaMonth *string
}

// Candidate is a card a rule proposes; Score orders them, higher first.
type Candidate struct {
	Kind       Kind       `json:"kind"`
	Tone       Tone       `json:"tone"`
	Icon       Icon       `json:"icon"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	CategoryID *uuid.UUID `json:"category_id"`
	GoalID     *uuid.UUID `json:"goal_id"`
	Score      int        `json:"score"`
}

// Rule proposes cards from a snapshot.
type Rule interface {
	Evaluate(ctx context.Context, s Snapshot) []Candidate
}

// Engine runs the rules and picks the cards to show.
type Engine struct {
	rules []Rule
}

// NewEngine returns an engine with the given rules; none gives every rule
// Sprout has.
func NewEngine(rules ...Rule) *Engine {
	if len(rules) == 0 {
		rules = DefaultRules()
	}
	return &Engine{rules: rules}
}

// DefaultRules is one rule for every kind of card in contract §2.7.
func DefaultRules() []Rule {
	return []Rule{
		categoryTrendRule{}, overBudgetRule{}, nearBudgetRule{},
		peakWeekdayRule{}, totalTrendRule{}, goalRule{},
	}
}

// Evaluate returns the cards for the snapshot, most relevant first
// (PRD BE-53): a period with fewer than MinTransactions expenses gets the
// single no_data card; otherwise the top MaxCards by score, at most one per
// category and one per goal, with at least one positive card when any rule
// found good news.
func (e *Engine) Evaluate(ctx context.Context, s Snapshot) []Candidate {
	if s.TxnCount < MinTransactions {
		return []Candidate{noData(s)}
	}
	var all []Candidate
	for _, rule := range e.rules {
		all = append(all, rule.Evaluate(ctx, s)...)
	}
	return Select(all)
}

// Select picks the cards to show from all the candidates.
func Select(all []Candidate) []Candidate {
	slices.SortStableFunc(all, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Title, b.Title))
	})
	seenCategory := map[uuid.UUID]bool{}
	seenGoal := map[uuid.UUID]bool{}
	var picked, positives []Candidate
	for _, c := range all {
		if len(picked) == MaxCards && (c.Tone != TonePositive || len(positives) > 0) {
			continue
		}
		if c.CategoryID != nil {
			if seenCategory[*c.CategoryID] {
				continue
			}
			seenCategory[*c.CategoryID] = true
		}
		if c.GoalID != nil {
			if seenGoal[*c.GoalID] {
				continue
			}
			seenGoal[*c.GoalID] = true
		}
		if len(picked) < MaxCards {
			picked = append(picked, c)
		} else if c.Tone == TonePositive {
			positives = append(positives, c)
		}
	}
	if len(positives) > 0 && !slices.ContainsFunc(picked, func(c Candidate) bool { return c.Tone == TonePositive }) {
		picked[len(picked)-1] = positives[0]
	}
	if picked == nil {
		picked = []Candidate{}
	}
	return picked
}
