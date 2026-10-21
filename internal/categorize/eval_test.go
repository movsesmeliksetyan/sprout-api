package categorize_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
)

// The quality bar of PRD §5.5.
const (
	minCoverage        = 0.60
	minPrecision       = 0.95
	minLLMAccuracy     = 0.90
	minKindF1          = 0.95
	labelledPath       = "../../testdata/categorize/labelled.jsonl"
	categoryTypesCount = 14
)

// labelled is one row of the labelled set.
type labelled struct {
	Bank         string  `json:"bank"`
	Lang         string  `json:"lang"`
	Description  string  `json:"description"`
	MCC          string  `json:"mcc"`
	AmountMinor  int64   `json:"amount_minor"`
	Direction    string  `json:"direction"`
	CategoryType *string `json:"category_type"`
	Kind         string  `json:"kind"`
}

func loadLabelled(t *testing.T) []labelled {
	t.Helper()
	f, err := os.Open(labelledPath)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var rows []labelled
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var row labelled
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &row), "%s", scanner.Text())
		rows = append(rows, row)
	}
	require.NoError(t, scanner.Err())
	return rows
}

// evaluation is what a run over the labelled set found.
type evaluation struct {
	expenses, covered, correct int
	// confusion counts results by labelled type, then by the type given
	// ("none" when no layer placed the row).
	confusion map[string]map[string]int
	bySource  map[categorize.Source]int
	misses    []string
}

func (e evaluation) coverage() float64  { return float64(e.covered) / float64(max(e.expenses, 1)) }
func (e evaluation) precision() float64 { return float64(e.correct) / float64(max(e.covered, 1)) }
func (e evaluation) accuracy() float64  { return float64(e.correct) / float64(max(e.expenses, 1)) }

// evaluate classifies the labelled expenses for a user who has one
// category of every type, so a result's category is its type.
func evaluate(t *testing.T, l ledger, rows []labelled, opts categorize.Options) evaluation {
	t.Helper()
	typeOf := map[uuid.UUID]string{}
	var categoryTypes []string
	require.NoError(t, func() error {
		r, err := l.pool.Query(context.Background(), `SELECT key FROM category_types ORDER BY key`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var key string
			if err := r.Scan(&key); err != nil {
				return err
			}
			categoryTypes = append(categoryTypes, key)
		}
		return r.Err()
	}())
	require.Len(t, categoryTypes, categoryTypesCount)
	for _, categoryType := range categoryTypes {
		typeOf[l.category(t, "My "+categoryType, categoryType)] = categoryType
	}

	var in []categorize.Input
	var want []string
	for _, row := range rows {
		if row.Kind != string(categorize.KindExpense) || row.CategoryType == nil {
			continue
		}
		in = append(in, categorize.Input{RawDescription: row.Description, MCC: row.MCC, AmountMinor: row.AmountMinor, Direction: categorize.DirectionDebit})
		want = append(want, *row.CategoryType)
	}
	results, err := l.engine.Classify(context.Background(), l.user.ID, in, opts)
	require.NoError(t, err)

	e := evaluation{expenses: len(in), confusion: map[string]map[string]int{}, bySource: map[categorize.Source]int{}}
	for i, r := range results {
		got := "none"
		if r.CategoryID != nil {
			got = typeOf[*r.CategoryID]
			e.covered++
			if got == want[i] {
				e.correct++
			} else {
				e.misses = append(e.misses, fmt.Sprintf("%q: %s by %s, want %s", in[i].RawDescription, got, r.Source, want[i]))
			}
		}
		e.bySource[r.Source]++
		if e.confusion[want[i]] == nil {
			e.confusion[want[i]] = map[string]int{}
		}
		e.confusion[want[i]][got]++
	}
	return e
}

// report is the evaluation as Markdown.
func (e evaluation) report(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", title)
	fmt.Fprintf(&b, "%d expense rows: coverage %.1f %%, precision on covered rows %.1f %%, top-1 accuracy %.1f %%.\n\n",
		e.expenses, 100*e.coverage(), 100*e.precision(), 100*e.accuracy())
	var sources []string
	for source, n := range e.bySource {
		sources = append(sources, fmt.Sprintf("%s %d", source, n))
	}
	sort.Strings(sources)
	fmt.Fprintf(&b, "By source: %s.\n\n", strings.Join(sources, ", "))
	var types []string
	for categoryType := range e.confusion {
		types = append(types, categoryType)
	}
	sort.Strings(types)
	b.WriteString("| Labelled | Rows | Right | Wrong | None |\n|---|---:|---:|---:|---:|\n")
	for _, categoryType := range types {
		row := e.confusion[categoryType]
		total, wrong := 0, 0
		for got, n := range row {
			total += n
			if got != categoryType && got != "none" {
				wrong += n
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", categoryType, total, row[categoryType], wrong, row["none"])
	}
	if len(e.misses) > 0 {
		b.WriteString("\nWrong answers:\n\n")
		for _, miss := range e.misses {
			fmt.Fprintf(&b, "- %s\n", miss)
		}
	}
	return b.String()
}

// kindF1 is the macro F1 of kind detection over the labelled rows, each
// detected on its own.
func kindF1(t *testing.T, rows []labelled) float64 {
	t.Helper()
	tp, fp, fn := map[categorize.Kind]int{}, map[categorize.Kind]int{}, map[categorize.Kind]int{}
	for _, row := range rows {
		direction := categorize.DirectionDebit
		if row.Direction == "credit" {
			direction = categorize.DirectionCredit
		}
		got, err := categorize.DetectKinds([]categorize.KindInput{{RawDescription: row.Description, AmountMinor: row.AmountMinor, Direction: direction}}, holder, nil)
		require.NoError(t, err)
		want := categorize.Kind(row.Kind)
		if got[0].Kind == want {
			tp[want]++
		} else {
			fp[got[0].Kind]++
			fn[want]++
			t.Logf("kind of %q: got %s, want %s", row.Description, got[0].Kind, want)
		}
	}
	kinds := []categorize.Kind{categorize.KindExpense, categorize.KindIncome, categorize.KindTransfer}
	var f1 float64
	for _, kind := range kinds {
		precision := float64(tp[kind]) / float64(max(tp[kind]+fp[kind], 1))
		recall := float64(tp[kind]) / float64(max(tp[kind]+fn[kind], 1))
		if precision+recall > 0 {
			f1 += 2 * precision * recall / (precision + recall) / float64(len(kinds))
		}
	}
	return f1
}

func TestEvalLabelledSet(t *testing.T) {
	t.Parallel()
	rows := loadLabelled(t)
	banks, langs := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.Kind == string(categorize.KindExpense) {
			banks[row.Bank], langs[row.Lang] = true, true
		}
	}
	assert.GreaterOrEqual(t, len(rows), 500)
	assert.GreaterOrEqual(t, len(banks), 6, "bank formats")
	assert.GreaterOrEqual(t, len(langs), 3, "languages")
}

// TestEvalDeterministic is `make eval`, and part of `make check`: layers 1–4
// over the labelled set must keep to the quality bar.
func TestEvalDeterministic(t *testing.T) {
	t.Parallel()
	rows := loadLabelled(t)
	e := evaluate(t, newLedger(t), rows, categorize.Options{})
	t.Log("\n" + e.report("Deterministic layers"))
	assert.GreaterOrEqual(t, e.coverage(), minCoverage, "coverage")
	assert.GreaterOrEqual(t, e.precision(), minPrecision, "precision on covered rows")

	f1 := kindF1(t, rows)
	t.Logf("kind detection macro F1 %.3f", f1)
	assert.GreaterOrEqual(t, f1, minKindF1)
}
