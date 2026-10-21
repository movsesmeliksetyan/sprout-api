package statements_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

func day(d int) time.Time { return time.Date(2025, time.July, d, 0, 0, 0, 0, time.UTC) }

func readable(d int, amount int64, dir statements.Direction, balance *int64) statements.Row {
	return statements.Row{NormalizedTransaction: statements.NormalizedTransaction{
		PostedAt: day(d), AmountMinor: amount, Direction: dir, BalanceAfter: balance,
	}}
}

func bal(v int64) *int64 { return &v }

func directions(rows []statements.Row) []statements.Direction {
	out := make([]statements.Direction, len(rows))
	for i, r := range rows {
		out[i] = r.Direction
	}
	return out
}

func TestValidate_ParseRateAndEmpty(t *testing.T) {
	t.Parallel()
	_, err := statements.Validate(nil, statements.Window{}, time.UTC)
	require.ErrorIs(t, err, statements.ErrNoRows)

	bad := statements.Row{Error: "no date"}
	_, err = statements.Validate([]statements.Row{readable(1, 100, statements.Debit, nil), bad, bad}, statements.Window{}, time.UTC)
	require.ErrorIs(t, err, statements.ErrLowParseRate, "a third readable")

	got, err := statements.Validate([]statements.Row{readable(1, 100, statements.Debit, nil), bad}, statements.Window{}, time.UTC)
	require.NoError(t, err, "half readable is enough")
	assert.Len(t, got.Rows, 2, "rows that could not be read are kept")
}

func TestValidate_Reconciliation(t *testing.T) {
	t.Parallel()
	D, C := statements.Debit, statements.Credit
	for name, tc := range map[string]struct {
		rows    []statements.Row
		flipped bool
	}{
		"consistent, oldest first": {rows: []statements.Row{
			readable(1, 1000, D, bal(9000)), readable(2, 500, C, bal(9500)), readable(3, 200, D, bal(9300)),
		}},
		"consistent, newest first": {rows: []statements.Row{
			readable(3, 200, D, bal(9300)), readable(2, 500, C, bal(9500)), readable(1, 1000, D, bal(9000)),
		}},
		"inverted, oldest first": {flipped: true, rows: []statements.Row{
			readable(1, 1000, C, bal(9000)), readable(2, 500, D, bal(9500)), readable(3, 200, C, bal(9300)),
		}},
		"inverted, newest first": {flipped: true, rows: []statements.Row{
			readable(3, 200, C, bal(9300)), readable(2, 500, D, bal(9500)), readable(1, 1000, C, bal(9000)),
		}},
		"one step is not enough to judge": {rows: []statements.Row{
			readable(1, 1000, C, bal(9000)), readable(2, 500, D, bal(9500)),
		}},
		"balances that explain neither are left alone": {rows: []statements.Row{
			readable(1, 1000, C, bal(9000)), readable(2, 500, D, bal(1)), readable(3, 200, C, bal(77)),
		}},
		"no balances": {rows: []statements.Row{
			readable(1, 1000, C, nil), readable(2, 500, D, nil), readable(3, 200, C, nil),
		}},
	} {
		got, err := statements.Validate(tc.rows, statements.Window{}, time.UTC)
		require.NoError(t, err, name)
		assert.Equal(t, tc.flipped, got.Flipped, name)
		want := directions(tc.rows)
		if tc.flipped {
			for i, d := range want {
				want[i] = map[statements.Direction]statements.Direction{D: C, C: D}[d]
			}
		}
		assert.Equal(t, want, directions(got.Rows), name)
	}
}

func TestValidate_Window(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	// Wednesday 16 July 2025: the week is Monday 14 to Sunday 20.
	week, err := statements.NewWindow("week", loc, time.Date(2025, time.July, 16, 12, 0, 0, 0, loc))
	require.NoError(t, err)

	rows := []statements.Row{
		readable(13, 100, statements.Debit, nil),
		{NormalizedTransaction: statements.NormalizedTransaction{PostedAt: time.Date(2025, time.July, 13, 23, 30, 0, 0, time.UTC),
			AmountMinor: 100, Direction: statements.Debit}}, // 00:30 on the 14th in London
		readable(16, 100, statements.Debit, nil),
		readable(21, 100, statements.Debit, nil),
		{Error: "no date"},
	}
	got, err := statements.Validate(rows, week, loc)
	require.NoError(t, err)
	assert.Equal(t, 2, got.OutOfPeriod)
	require.Len(t, got.Rows, 3)
	assert.Equal(t, "no date", got.Rows[2].Error)

	all, err := statements.NewWindow("all", loc, time.Now())
	require.NoError(t, err)
	got, err = statements.Validate(rows, all, loc)
	require.NoError(t, err)
	assert.Zero(t, got.OutOfPeriod)
	assert.Len(t, got.Rows, 5)

	_, err = statements.NewWindow("decade", loc, time.Now())
	assert.Error(t, err)
}

func TestValidate_RowCap(t *testing.T) {
	t.Parallel()
	rows := make([]statements.Row, statements.MaxRows+1)
	for i := range rows {
		rows[i] = readable(1+i%28, 100, statements.Debit, nil)
	}
	_, err := statements.Validate(rows, statements.Window{}, time.UTC)
	require.ErrorIs(t, err, statements.ErrTooManyRows)

	// The cap is on what the window keeps.
	month, err := statements.NewWindow("month", time.UTC, time.Date(2025, time.August, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	got, err := statements.Validate(append(rows, readable(1, 100, statements.Debit, nil)), month, time.UTC)
	require.NoError(t, err)
	assert.Empty(t, got.Rows)
}

// The fixture's bank writes money out as positive and money in as
// negative; the heuristics read it the usual way round, and the balances
// put it right.
func TestValidate_InvertedSignFixture(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../testdata/statements/validate/inverted_sign.csv")
	require.NoError(t, err)
	detected, err := statements.Detect(data)
	require.NoError(t, err)
	table, err := statements.ExtractCSV(detected)
	require.NoError(t, err)
	spec, _ := statements.Heuristic(table, statements.Hints{})
	rows := statements.Transform(table, spec, time.UTC)

	got, err := statements.Validate(rows, statements.Window{}, time.UTC)
	require.NoError(t, err)
	assert.True(t, got.Flipped)
	for _, row := range got.Rows {
		want := statements.Debit
		if strings.Contains(row.RawDescription, "SALARY") || strings.Contains(row.RawDescription, "REFUND") {
			want = statements.Credit
		}
		assert.Equal(t, want, row.Direction, row.RawDescription)
	}
}
