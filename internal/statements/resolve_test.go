package statements_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// table reads a CSV or XLSX fixture into its table.
func table(t *testing.T, path string) statements.RawTable {
	t.Helper()
	data := fixture(t, path)
	if strings.HasSuffix(path, ".xlsx") {
		out, err := statements.ExtractXLSX(data)
		require.NoError(t, err)
		return out
	}
	detected, err := statements.Detect(data)
	require.NoError(t, err)
	out, err := statements.ExtractCSV(detected)
	require.NoError(t, err)
	return out
}

func TestHeuristic_RealLayouts(t *testing.T) {
	t.Parallel()
	type want struct {
		date        int
		format      string
		description []int
		amount      *int
		debit       *int
		credit      *int
		direction   *int
		decimal     string
	}
	tests := map[string]want{
		"csv/uk-signed.csv":       {0, "DD/MM/YYYY", []int{1}, col(2), nil, nil, nil, "."},
		"csv/uk-paid-in-out.csv":  {0, "DD MMM YYYY", []int{2}, nil, col(3), col(4), nil, "."},
		"csv/de-semicolon.csv":    {0, "DD.MM.YYYY", []int{2}, col(3), nil, nil, nil, ","},
		"csv/es-tab.csv":          {0, "DD/MM/YYYY", []int{1}, col(2), nil, nil, nil, ","},
		"csv/fr-footer.csv":       {0, "DD/MM/YYYY", []int{1}, nil, col(2), col(3), nil, ","},
		"csv/nl-quoted.csv":       {0, "YYYYMMDD", []int{1, 5}, col(4), nil, nil, col(3), ","},
		"csv/multiline.csv":       {0, "YYYY-MM-DD", []int{1}, col(2), nil, nil, nil, "."},
		"csv/stray-quotes.csv":    {0, "YYYY-MM-DD", []int{1}, col(2), nil, nil, nil, "."},
		"csv/ragged.csv":          {0, "YYYY-MM-DD", []int{1}, col(2), nil, nil, nil, "."},
		"csv/monzo.csv":           {1, "DD/MM/YYYY", []int{4, 14}, col(7), nil, nil, nil, "."},
		"csv/us-chase.csv":        {1, "MM/DD/YYYY", []int{2}, col(3), nil, nil, col(0), "."},
		"csv/am-ameria.csv":       {0, "DD/MM/YYYY", []int{1}, col(2), nil, nil, nil, "."},
		"xlsx/bank-dates.xlsx":    {0, "YYYY-MM-DD", []int{1}, col(2), nil, nil, nil, "."},
		"xlsx/merged-header.xlsx": {0, "YYYY-MM-DD", []int{1}, nil, col(2), col(3), nil, "."},
	}
	require.GreaterOrEqual(t, len(tests), 12)
	for path, w := range tests {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			tbl := table(t, path)
			spec, confidence := statements.Heuristic(tbl, statements.Hints{})
			assert.GreaterOrEqual(t, confidence, statements.HeuristicThreshold)
			require.NoError(t, spec.Check(tbl, 0))
			assert.Equal(t, w.date, spec.Columns.Date)
			assert.Equal(t, w.format, spec.DateFormat)
			assert.Equal(t, w.description, spec.Columns.Description)
			assert.Equal(t, w.amount, spec.Columns.Amount)
			assert.Equal(t, w.debit, spec.Columns.Debit)
			assert.Equal(t, w.credit, spec.Columns.Credit)
			assert.Equal(t, w.direction, spec.Columns.Direction)
			assert.Equal(t, w.decimal, spec.DecimalSeparator)
			for _, row := range statements.Transform(tbl, spec, london) {
				assert.Empty(t, row.Error, "row %d", row.RowIndex)
			}
		})
	}
}

func TestHeuristic_Directions(t *testing.T) {
	t.Parallel()
	nl := table(t, "csv/nl-quoted.csv")
	spec, _ := statements.Heuristic(nl, statements.Hints{})
	rows := statements.Transform(nl, spec, london)
	assert.Equal(t, []statements.Direction{statements.Debit, statements.Debit, statements.Credit},
		[]statements.Direction{rows[0].Direction, rows[1].Direction, rows[2].Direction}, "Af and Bij")
}

func TestInferDateOrder(t *testing.T) {
	t.Parallel()
	ambiguous := statements.RawTable{
		Headers: []string{"Date", "Description", "Amount"},
		Rows:    [][]string{{"04/07/2025", "A", "-1.00"}, {"03/07/2025", "B", "-2.00"}},
	}
	spec, confidence := statements.Heuristic(ambiguous, statements.Hints{})
	assert.Equal(t, "DD/MM/YYYY", spec.DateFormat)
	assert.Less(t, confidence, statements.HeuristicThreshold, "a guess is not enough to go on")

	monthFirst := true
	spec, confidence = statements.Heuristic(ambiguous, statements.Hints{MonthFirst: &monthFirst})
	assert.Equal(t, "MM/DD/YYYY", spec.DateFormat)
	assert.GreaterOrEqual(t, confidence, statements.HeuristicThreshold, "the user's locale settles it")
}

func TestHeuristic_GivesUp(t *testing.T) {
	t.Parallel()
	for name, tbl := range map[string]statements.RawTable{
		"no dates":   {Headers: []string{"a", "b"}, Rows: [][]string{{"x", "1.00"}, {"y", "2.00"}}},
		"no amounts": {Headers: []string{"Date", "Text"}, Rows: [][]string{{"14/07/2025", "x"}, {"15/07/2025", "y"}}},
		"empty":      {},
	} {
		_, confidence := statements.Heuristic(tbl, statements.Hints{})
		assert.Zero(t, confidence, name)
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	assert.Equal(t, statements.Fingerprint([]string{"Buchungstag", "Betrag (EUR)"}), statements.Fingerprint([]string{" BUCHUNGSTAG ", "betrag  (eur)"}))
	assert.Equal(t, statements.Fingerprint([]string{"Libellé"}), statements.Fingerprint([]string{"libelle"}), "accents folded")
	assert.NotEqual(t, statements.Fingerprint([]string{"Date", "Amount"}), statements.Fingerprint([]string{"Amount", "Date"}), "order counts")
}

func TestResolver_SecondFileHitsTheTemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	calls := 0
	resolver := statements.NewResolver(db.New(pool), statements.WithHeuristic(func(tbl statements.RawTable, h statements.Hints) (statements.MappingSpec, int) {
		calls++
		return statements.Heuristic(tbl, h)
	}))

	first, err := resolver.Resolve(ctx, table(t, "csv/de-semicolon.csv"), statements.FormatCSV, statements.Hints{})
	require.NoError(t, err)
	assert.Equal(t, statements.SourceHeuristic, first.Source)
	assert.Equal(t, 1, calls)

	second, err := resolver.Resolve(ctx, table(t, "csv/de-semicolon.csv"), statements.FormatCSV, statements.Hints{})
	require.NoError(t, err)
	assert.Equal(t, statements.SourceTemplate, second.Source)
	assert.Equal(t, first.Spec, second.Spec)
	assert.Equal(t, 1, calls, "no heuristics the second time")
}

func TestResolver_TemplateRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	resolver := statements.NewResolver(db.New(pool))
	tbl := table(t, "csv/uk-signed.csv")
	fingerprint := statements.Fingerprint(tbl.Headers)
	good, _ := statements.Heuristic(tbl, statements.Hints{})

	// A user's mapping wins, and a later heuristic one does not replace it.
	users := good
	users.Columns.Balance = nil
	require.NoError(t, resolver.Save(ctx, fingerprint, statements.FormatCSV, nil, users, statements.SourceUser))
	require.NoError(t, resolver.Save(ctx, fingerprint, statements.FormatCSV, nil, good, statements.SourceHeuristic))
	got, err := resolver.Resolve(ctx, tbl, statements.FormatCSV, statements.Hints{})
	require.NoError(t, err)
	assert.Equal(t, statements.SourceTemplate, got.Source)
	assert.Nil(t, got.Spec.Columns.Balance, "the user's template")

	// A template that keeps failing is disabled: under 80 % over 5 uses.
	for _, ok := range []bool{true, false, false, true, true} {
		require.NoError(t, resolver.RecordUse(ctx, fingerprint, ok))
	}
	got, err = resolver.Resolve(ctx, tbl, statements.FormatCSV, statements.Hints{})
	require.NoError(t, err)
	assert.Equal(t, statements.SourceHeuristic, got.Source, "3 of 5 is 60 %: the template is passed over")
	var source string
	require.NoError(t, pool.QueryRow(ctx, `SELECT source FROM mapping_templates WHERE header_fingerprint = $1`, fingerprint).Scan(&source))
	assert.Equal(t, "heuristic", source, "and the disabled template is replaced")

	// A template that does not read the file falls through.
	broken := good
	broken.DateFormat = "MM/DD/YYYY"
	other := table(t, "csv/es-tab.csv")
	otherPrint := statements.Fingerprint(other.Headers)
	require.NoError(t, resolver.Save(ctx, otherPrint, statements.FormatCSV, nil, broken, statements.SourceLLM))
	got, err = resolver.Resolve(ctx, other, statements.FormatCSV, statements.Hints{})
	require.NoError(t, err)
	assert.Equal(t, statements.SourceHeuristic, got.Source)

	// Neither: no mapping.
	_, err = resolver.Resolve(ctx, statements.RawTable{Headers: []string{"x", "y"}, Rows: [][]string{{"a", "b"}}}, statements.FormatCSV, statements.Hints{})
	assert.ErrorIs(t, err, statements.ErrNoMapping)
}
