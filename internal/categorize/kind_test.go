package categorize_test

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

const holder = "Alex Rivera"

type kindRow struct {
	file string
	in   categorize.KindInput
	kind categorize.Kind
	note string
}

func loadKindFixture(t *testing.T) []kindRow {
	t.Helper()
	f, err := os.Open("../../testdata/categorize/kinds.tsv")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var rows []kindRow
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		require.Len(t, fields, 6, "line %d", line)
		date, err := time.Parse(time.DateOnly, fields[1])
		require.NoError(t, err, "line %d", line)
		amount, err := strconv.ParseInt(fields[2], 10, 64)
		require.NoError(t, err, "line %d", line)
		direction := categorize.DirectionDebit
		if amount > 0 {
			direction = categorize.DirectionCredit
		}
		rows = append(rows, kindRow{
			file: fields[0],
			in: categorize.KindInput{
				RawDescription: fields[3], AmountMinor: max(amount, -amount), Direction: direction, OccurredAt: date,
			},
			kind: categorize.Kind(fields[4]),
			note: fields[5],
		})
	}
	require.NoError(t, scanner.Err())
	return rows
}

func TestDetectKinds_LabelledFixture(t *testing.T) {
	t.Parallel()
	rows := loadKindFixture(t)
	require.GreaterOrEqual(t, len(rows), 70)

	byFile := map[string][]int{}
	var files []string
	for i, row := range rows {
		if _, ok := byFile[row.file]; !ok {
			files = append(files, row.file)
		}
		byFile[row.file] = append(byFile[row.file], i)
	}

	kinds := []categorize.Kind{categorize.KindExpense, categorize.KindIncome, categorize.KindTransfer}
	tp, fp, fn := map[categorize.Kind]int{}, map[categorize.Kind]int{}, map[categorize.Kind]int{}
	for _, file := range files {
		in := make([]categorize.KindInput, len(byFile[file]))
		for j, i := range byFile[file] {
			in[j] = rows[i].in
		}
		got, err := categorize.DetectKinds(in, holder, nil)
		require.NoError(t, err)
		for j, i := range byFile[file] {
			want := rows[i]
			if got[j].Kind == want.kind {
				tp[want.kind]++
			} else {
				fp[got[j].Kind]++
				fn[want.kind]++
				t.Logf("%s %q: got %s, want %s", file, want.in.RawDescription, got[j].Kind, want.kind)
			}
			assert.Equal(t, want.note, got[j].Note, "%s %q note", file, want.in.RawDescription)
			assert.Equal(t, got[j].Kind != categorize.KindTransfer, got[j].Included, "%q included", want.in.RawDescription)
		}
	}

	var f1 float64
	for _, kind := range kinds {
		precision := float64(tp[kind]) / float64(max(tp[kind]+fp[kind], 1))
		recall := float64(tp[kind]) / float64(max(tp[kind]+fn[kind], 1))
		if precision+recall > 0 {
			f1 += 2 * precision * recall / (precision + recall) / float64(len(kinds))
		}
	}
	t.Logf("kind detection macro F1 %.3f over %d rows", f1, len(rows))
	assert.GreaterOrEqual(t, f1, 0.95)
}

func TestDetectKinds_Cases(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	debit := func(description string, amount int64, at time.Time) categorize.KindInput {
		return categorize.KindInput{RawDescription: description, AmountMinor: amount, Direction: categorize.DirectionDebit, OccurredAt: at}
	}
	credit := func(description string, amount int64, at time.Time) categorize.KindInput {
		return categorize.KindInput{RawDescription: description, AmountMinor: amount, Direction: categorize.DirectionCredit, OccurredAt: at}
	}

	got, err := categorize.DetectKinds([]categorize.KindInput{
		credit("SALARY ACME LTD", 250000, day),
		debit("CREDIT CARD REPAYMENT", 30000, day),
		debit("ARGOS", 4000, day),
		credit("ARGOS", 4000, day.Add(24*time.Hour)),
		debit("ONLINE PAYMENT REF 1ABC22", 9000, day),
		credit("BANK CREDIT REF 9XYZ11", 9000, day.Add(47*time.Hour)),
		debit("SHOP ONE", 7000, day),
		credit("SOMEONE ELSE", 7000, day.Add(49*time.Hour)),
		credit("RIVERA ALEX", 100, day),
		credit("ALEX", 100, day.Add(10*24*time.Hour)),
	}, holder, nil)
	require.NoError(t, err)
	assert.Equal(t, []categorize.KindResult{
		{Kind: categorize.KindIncome, Included: true},
		{Kind: categorize.KindTransfer},
		{Kind: categorize.KindExpense, Included: true},
		{Kind: categorize.KindIncome, Included: true, Note: categorize.RefundNote},
		{Kind: categorize.KindTransfer},
		{Kind: categorize.KindTransfer},
		{Kind: categorize.KindExpense, Included: true},
		{Kind: categorize.KindIncome, Included: true},
		{Kind: categorize.KindTransfer},
		{Kind: categorize.KindIncome, Included: true},
	}, got)

	// A one-word name matches nothing.
	got, err = categorize.DetectKinds([]categorize.KindInput{credit("ALEX", 100, day)}, "Alex", nil)
	require.NoError(t, err)
	assert.Equal(t, categorize.KindIncome, got[0].Kind)
}

func TestEngine_DetectKinds_UsesHistory(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	food := l.category(t, "Food", "food")
	testutil.NewTransaction(t, l.pool, l.user.User, testutil.TransactionExpense(food), testutil.TransactionMerchant("Waitrose"))
	other := testutil.NewTestUser(t, l.pool)
	otherFood := testutil.NewCategory(t, l.pool, other.ID, testutil.CategoryType("food")).ID
	testutil.NewTransaction(t, l.pool, other.User, testutil.TransactionExpense(otherFood), testutil.TransactionMerchant("Ocado"))

	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	got, err := l.engine.DetectKinds(context.Background(), l.user.ID, holder, []categorize.KindInput{
		{RawDescription: "WAITROSE 123 REFUND", AmountMinor: 500, Direction: categorize.DirectionCredit, OccurredAt: day},
		{RawDescription: "OCADO", AmountMinor: 500, Direction: categorize.DirectionCredit, OccurredAt: day},
	})
	require.NoError(t, err)
	assert.Equal(t, categorize.RefundNote, got[0].Note, "a merchant the user spent with before")
	assert.Empty(t, got[1].Note, "another user's history does not count")
}
