package insights

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	food     = uuid.MustParse("01900000-0000-7000-8000-000000000001")
	selfCare = uuid.MustParse("01900000-0000-7000-8000-000000000002")
	travel   = uuid.MustParse("01900000-0000-7000-8000-000000000003")
	bills    = uuid.MustParse("01900000-0000-7000-8000-000000000004")
	house    = uuid.MustParse("01900000-0000-7000-8000-000000000101")
)

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func ptr[T any](v T) *T { return &v }

// july is July 2025 seen on the 21st.
func july(t *testing.T) period.Range {
	t.Helper()
	r, err := period.Resolve(period.Month, 0, time.UTC, day("2025-07-21").Add(12*time.Hour))
	require.NoError(t, err)
	return r
}

// design is the month the app's design shows: less on food than in June,
// self-care past its budget mostly through one spa visit, and Saturdays
// taking the most.
func design(t *testing.T) Snapshot {
	return Snapshot{
		Range: july(t), Today: day("2025-07-21"), Currency: "USD", TxnCount: 41,
		TotalSpentMinor: 184250, PreviousTotalMinor: 189000,
		Categories: []Category{
			{ID: food, Name: "Food", SpentMinor: 124000, PreviousSpentMinor: 135000, BudgetMinor: 150000, TxnCount: 24},
			{ID: selfCare, Name: "Self-care", SpentMinor: 16000, PreviousSpentMinor: 15500, BudgetMinor: 15000, TxnCount: 3,
				Largest: &Expense{AmountMinor: 9000, Merchant: ptr("Serenity Spa"), LocalDate: day("2025-07-12")}},
			{ID: bills, Name: "Bills", SpentMinor: 44250, PreviousSpentMinor: 38500, BudgetMinor: 60000, TxnCount: 14},
		},
		Weekdays: [7]int64{20000, 22000, 18000, 21000, 23000, 30250, 50000},
	}
}

func TestEngine_TheDesign(t *testing.T) {
	t.Parallel()
	got := NewEngine().Evaluate(context.Background(), design(t))
	require.Len(t, got, 3)
	assert.Equal(t, "Self-care is over budget", got[0].Title)
	assert.Equal(t, "$10 above your $150 limit. Most of it was one $90 expense at Serenity Spa on the 12th.", got[0].Body)
	assert.Equal(t, "You spent 8% less on Food", got[1].Title)
	assert.Equal(t, "Your biggest day was Saturday", got[2].Title)
	assert.Equal(t, []Tone{ToneWarning, TonePositive, ToneNeutral}, []Tone{got[0].Tone, got[1].Tone, got[2].Tone})
}

func TestEngine_NoData(t *testing.T) {
	t.Parallel()
	s := design(t)
	s.TxnCount = 2
	got := NewEngine().Evaluate(context.Background(), s)
	require.Len(t, got, 1)
	assert.Equal(t, KindNoData, got[0].Kind)
	assert.Equal(t, "Add a few more expenses this month and your insights will show up here.", got[0].Body)
}

func card(kind Kind, tone Tone, score int, category *uuid.UUID) Candidate {
	return Candidate{Kind: kind, Tone: tone, Score: score, CategoryID: category, Title: fmt.Sprint(kind, score)}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	t.Run("one card per category, highest score first", func(t *testing.T) {
		got := Select([]Candidate{
			card(KindCategorySpendUp, ToneNeutral, 60, &food),
			card(KindCategoryOverBudget, ToneWarning, 95, &food),
			card(KindPeakWeekday, ToneNeutral, 50, nil),
		})
		require.Len(t, got, 2)
		assert.Equal(t, KindCategoryOverBudget, got[0].Kind)
		assert.Equal(t, KindPeakWeekday, got[1].Kind)
	})
	t.Run("one card per goal", func(t *testing.T) {
		almost := Candidate{Kind: KindGoalAlmostThere, Tone: TonePositive, Score: 75, GoalID: &house}
		onTrack := Candidate{Kind: KindGoalOnTrack, Tone: TonePositive, Score: 45, GoalID: &house}
		assert.Equal(t, []Candidate{almost}, Select([]Candidate{onTrack, almost}))
	})
	t.Run("at most five, keeping a positive one", func(t *testing.T) {
		ids := []uuid.UUID{food, selfCare, travel, bills, house}
		var all []Candidate
		for i, id := range ids {
			all = append(all, card(KindCategoryOverBudget, ToneWarning, 99-i, &id))
		}
		all = append(all, card(KindCategoryNearBudget, ToneWarning, 70, ptr(uuid.New())))
		all = append(all, card(KindGoalOnTrack, TonePositive, 45, nil), card(KindTotalSpendDown, TonePositive, 40, nil))
		got := Select(all)
		require.Len(t, got, MaxCards)
		assert.Equal(t, 99, got[0].Score)
		assert.Equal(t, KindGoalOnTrack, got[4].Kind, "the best positive card takes the last place")
	})
	t.Run("no positive card to keep", func(t *testing.T) {
		got := Select([]Candidate{card(KindPeakWeekday, ToneNeutral, 50, nil)})
		assert.Len(t, got, 1)
	})
	t.Run("nothing", func(t *testing.T) {
		assert.Equal(t, []Candidate{}, Select(nil))
	})
}

// TestRules runs every rule on its own over the snapshots in the table and
// compares what it proposes with testdata/insights/<rule>.golden.json.
func TestRules(t *testing.T) {
	t.Parallel()
	past := july(t)
	past.IsCurrent, past.Offset = false, 3
	early := design(t)
	early.Today = day("2025-07-04")
	year, err := period.Resolve(period.Year, 1, time.UTC, day("2025-07-21"))
	require.NoError(t, err)
	week, err := period.Resolve(period.Week, 0, time.UTC, day("2025-07-21"))
	require.NoError(t, err)

	type scenario struct {
		Name string
		Snap Snapshot
	}
	with := func(name string, change func(*Snapshot)) scenario {
		s := design(t)
		change(&s)
		return scenario{name, s}
	}
	for _, tc := range []struct {
		rule      string
		r         Rule
		scenarios []scenario
	}{
		{"category_trend", categoryTrendRule{}, []scenario{
			{"the design", design(t)},
			{"early in the month nothing is less yet", early},
			with("more on travel", func(s *Snapshot) {
				s.Categories = append(s.Categories, Category{ID: travel, Name: "Travel", SpentMinor: 30000, PreviousSpentMinor: 12000, TxnCount: 2})
			}),
			with("a small change is not news", func(s *Snapshot) { s.Categories[0].PreviousSpentMinor = 125500 }),
			with("nothing spent before", func(s *Snapshot) { s.Categories[0].PreviousSpentMinor = 0 }),
			with("a past month", func(s *Snapshot) { s.Range = past }),
		}},
		{"over_budget", overBudgetRule{}, []scenario{
			{"the design", design(t)},
			with("spread over many expenses", func(s *Snapshot) { s.Categories[1].Largest.AmountMinor = 4000 }),
			with("no merchant", func(s *Snapshot) { s.Categories[1].Largest.Merchant = nil }),
			with("no budget", func(s *Snapshot) { s.Categories[1].BudgetMinor = 0 }),
			with("in a year", func(s *Snapshot) { s.Range = year }),
		}},
		{"near_budget", nearBudgetRule{}, []scenario{
			with("food close to its budget", func(s *Snapshot) { s.Categories[0].SpentMinor = 140000 }),
			with("on the last day", func(s *Snapshot) { s.Categories[0].SpentMinor = 140000; s.Today = day("2025-07-31") }),
			with("a past month", func(s *Snapshot) { s.Categories[0].SpentMinor = 140000; s.Range = past }),
		}},
		{"peak_weekday", peakWeekdayRule{}, []scenario{
			{"the design", design(t)},
			with("an even month", func(s *Snapshot) { s.Weekdays = [7]int64{26000, 26000, 26000, 26250, 26000, 27000, 27000} }),
			with("a week", func(s *Snapshot) { s.Range = week }),
			with("a single day", func(s *Snapshot) { s.Weekdays = [7]int64{0, 0, 0, 0, 0, 0, 184250} }),
		}},
		{"total_trend", totalTrendRule{}, []scenario{
			{"the design: too small a change", design(t)},
			with("down", func(s *Snapshot) { s.PreviousTotalMinor = 205250 }),
			with("up", func(s *Snapshot) { s.PreviousTotalMinor = 140000 }),
			with("down in a past year", func(s *Snapshot) { s.Range = year; s.PreviousTotalMinor = 205250 }),
			with("nothing before", func(s *Snapshot) { s.PreviousTotalMinor = 0 }),
		}},
		{"goal", goalRule{}, []scenario{
			with("goals", func(s *Snapshot) {
				s.Goals = []Goal{
					{ID: house, Title: "House deposit", TargetMinor: 2000000, SavedMinor: 600000, MonthlyPaceMinor: 50000, EtaMonth: ptr("2027-11")},
					{ID: uuid.MustParse("01900000-0000-7000-8000-000000000102"), Title: "new bike", TargetMinor: 120000, SavedMinor: 110000},
					{ID: uuid.MustParse("01900000-0000-7000-8000-000000000103"), Title: "Done", TargetMinor: 1000, SavedMinor: 1000},
					{ID: uuid.MustParse("01900000-0000-7000-8000-000000000104"), Title: "Stalled", TargetMinor: 1000, SavedMinor: 100, MonthlyPaceMinor: -50},
				}
			}),
		}},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			t.Parallel()
			got := map[string][]Candidate{}
			var order []string
			for _, sc := range tc.scenarios {
				got[sc.Name] = tc.r.Evaluate(context.Background(), sc.Snap)
				order = append(order, sc.Name)
			}
			golden(t, filepath.Join("testdata", "insights", tc.rule+".golden.json"), order, got)
		})
	}
}

// golden compares the candidates of each scenario, in order, with a golden
// file; -update rewrites it.
func golden(t *testing.T, path string, order []string, got map[string][]Candidate) {
	t.Helper()
	type entry struct {
		Scenario string      `json:"scenario"`
		Cards    []Candidate `json:"cards"`
	}
	entries := make([]entry, len(order))
	for i, name := range order {
		entries[i] = entry{name, got[name]}
	}
	encoded, err := json.MarshalIndent(entries, "", "  ")
	require.NoError(t, err)
	encoded = append(encoded, '\n')
	path = filepath.Join("..", "..", path)
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, encoded, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -update to write it")
	assert.Equal(t, string(want), string(encoded))
}
