package summary_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func (f fixture) stats(t *testing.T, user testutil.TestUser, query string) httpx.StatsSummary {
	t.Helper()
	rec := f.get(t, user, "/summary/stats?"+query)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.StatsSummary](t, rec)
}

// slice is a BreakdownItem in a form that compares and prints well; the
// "Other" row has the nil id.
type slice struct {
	CategoryID  uuid.UUID
	AmountMinor int64
	SharePct    int
}

func slices(stats httpx.StatsSummary) []slice {
	out := make([]slice, 0, len(stats.Breakdown))
	for _, row := range stats.Breakdown {
		s := slice{AmountMinor: row.AmountMinor, SharePct: row.SharePct}
		if row.CategoryID != nil {
			s.CategoryID = *row.CategoryID
		}
		out = append(out, s)
	}
	return out
}

func slicesTotal(stats httpx.StatsSummary) int {
	total := 0
	for _, row := range stats.Breakdown {
		total += row.SharePct
	}
	return total
}

// TestGetStatsSummary_TheDesign reproduces the Stats screen of the design:
// $1,842.50 in July, $210 less than in June.
func TestGetStatsSummary_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	stats := f.stats(t, user, "period=month")

	assert.Equal(t, "2025-07-01", stats.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-31", stats.Range.End.Format(time.DateOnly))
	assert.True(t, stats.Range.IsCurrent)
	assert.EqualValues(t, 184250, stats.TotalSpentMinor)
	assert.EqualValues(t, 205250, stats.PreviousTotalMinor)
	assert.EqualValues(t, -21000, stats.DeltaMinor)
	assert.Equal(t, pct(-10), stats.DeltaPct)
	assert.Equal(t, httpx.BucketUnit("week"), stats.Series.Unit)
	assert.Equal(t, []string{
		"2025-07-01/2025-07-07", "2025-07-08/2025-07-14", "2025-07-15/2025-07-21", "2025-07-22/2025-07-31",
	}, spans(stats.Series.Buckets))
	assert.Equal(t, []int64{95500, 29500, 59250, 0}, amounts(stats.Series.Buckets))
	assert.Equal(t, 0, stats.Series.PeakIndex)
	assert.Equal(t, []slice{
		{d.housing, 60000, 33},
		{d.food, 48250, 26},
		{d.transport, 21000, 11},
		{d.selfCare, 16000, 9},
		{d.utilities, 14500, 8},
		{uuid.Nil, 24500, 13},
	}, slices(stats), "Leisure and Health make up the last row")
	assert.Equal(t, 100, slicesTotal(stats))
}

func TestGetStatsSummary_Week(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	stats := f.stats(t, user, "period=week")

	assert.Equal(t, "2025-07-21", stats.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-27", stats.Range.End.Format(time.DateOnly))
	assert.EqualValues(t, 2440, stats.TotalSpentMinor)
	assert.EqualValues(t, 72810, stats.PreviousTotalMinor)
	assert.EqualValues(t, -70370, stats.DeltaMinor)
	assert.Equal(t, pct(-97), stats.DeltaPct)
	assert.Equal(t, httpx.BucketUnit("day"), stats.Series.Unit)
	assert.Equal(t, []int64{2440, 0, 0, 0, 0, 0, 0}, amounts(stats.Series.Buckets))
	assert.Equal(t, 0, stats.Series.PeakIndex)
	assert.Equal(t, []slice{{d.food, 2440, 100}}, slices(stats))
}

func TestGetStatsSummary_Year(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)
	f.spend(t, user, d.food, 100000, 2024, time.December, 31)

	stats := f.stats(t, user, "period=year")

	assert.Equal(t, "2025-01-01", stats.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-12-31", stats.Range.End.Format(time.DateOnly))
	assert.EqualValues(t, 389500, stats.TotalSpentMinor)
	assert.EqualValues(t, 100000, stats.PreviousTotalMinor)
	assert.EqualValues(t, 289500, stats.DeltaMinor)
	assert.Equal(t, pct(290), stats.DeltaPct)
	assert.Equal(t, httpx.BucketUnit("month"), stats.Series.Unit)
	assert.Equal(t, []int64{0, 0, 0, 0, 0, 205250, 184250, 0, 0, 0, 0, 0}, amounts(stats.Series.Buckets))
	assert.Equal(t, 5, stats.Series.PeakIndex)
	assert.Equal(t, []slice{
		{d.housing, 120000, 31},
		{d.transport, 114000, 29},
		{d.food, 100500, 26},
		{d.selfCare, 16000, 4},
		{d.utilities, 14500, 4},
		{uuid.Nil, 24500, 6},
	}, slices(stats))
	assert.Equal(t, 100, slicesTotal(stats))
}

// TestGetStatsSummary_PastMonth reads June, which has three categories and
// nothing before it.
func TestGetStatsSummary_PastMonth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	stats := f.stats(t, user, "period=month&offset=1")

	assert.Equal(t, 1, stats.Range.Offset)
	assert.False(t, stats.Range.IsCurrent)
	assert.EqualValues(t, 205250, stats.TotalSpentMinor)
	assert.Zero(t, stats.PreviousTotalMinor)
	assert.EqualValues(t, 205250, stats.DeltaMinor)
	assert.Nil(t, stats.DeltaPct, "there is nothing to compare with")
	assert.Equal(t, []int64{60000, 52250, 0, 93000}, amounts(stats.Series.Buckets))
	assert.Equal(t, 3, stats.Series.PeakIndex)
	assert.Equal(t, []slice{
		{d.transport, 93000, 45},
		{d.housing, 60000, 29},
		{d.food, 52250, 26},
	}, slices(stats), "fewer than six categories leave no last row")
}

func TestGetStatsSummary_Breakdown(t *testing.T) {
	t.Parallel()

	// spendEach gives a user one category per amount, in display order, each
	// with that much spent this month.
	spendEach := func(t *testing.T, f fixture, user testutil.TestUser, amounts ...int64) []uuid.UUID {
		t.Helper()
		ids := make([]uuid.UUID, 0, len(amounts))
		for _, amount := range amounts {
			category := testutil.NewCategory(t, f.pool, user.ID)
			f.spend(t, user, category.ID, amount, 2025, time.July, 2)
			ids = append(ids, category.ID)
		}
		return ids
	}

	t.Run("five categories", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		user := testutil.NewTestUser(t, f.pool)
		ids := spendEach(t, f, user, 100, 200, 300, 400, 500)
		testutil.NewCategory(t, f.pool, user.ID) // never used

		stats := f.stats(t, user, "period=month")

		assert.Equal(t, []slice{
			{ids[4], 500, 33}, {ids[3], 400, 27}, {ids[2], 300, 20}, {ids[1], 200, 13}, {ids[0], 100, 7},
		}, slices(stats), "a category without spend is not a row")
	})

	t.Run("six categories, the last two equal", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		user := testutil.NewTestUser(t, f.pool)
		ids := spendEach(t, f, user, 500, 400, 300, 200, 100, 100)

		stats := f.stats(t, user, "period=month")

		assert.Equal(t, []slice{
			{ids[0], 500, 31}, {ids[1], 400, 25}, {ids[2], 300, 19}, {ids[3], 200, 13}, {ids[4], 100, 6},
			{uuid.Nil, 100, 6},
		}, slices(stats), "of two equal categories the earlier in display order is named")
		assert.Equal(t, 100, slicesTotal(stats))
	})

	t.Run("the rest outweigh the fifth", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		user := testutil.NewTestUser(t, f.pool)
		ids := spendEach(t, f, user, 500, 400, 300, 200, 100, 90, 80)

		stats := f.stats(t, user, "period=month")

		assert.Equal(t, []slice{
			{ids[0], 500, 30}, {ids[1], 400, 24}, {ids[2], 300, 18}, {ids[3], 200, 12}, {ids[4], 100, 6},
			{uuid.Nil, 170, 10},
		}, slices(stats), "the last row stays last")
		assert.Equal(t, 100, slicesTotal(stats))
	})
}

func TestGetStatsSummary_PeakTieIsTheFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	first := testutil.NewCategory(t, f.pool, user.ID)
	second := testutil.NewCategory(t, f.pool, user.ID)
	f.spend(t, user, first.ID, 700, 2025, time.July, 15)
	f.spend(t, user, first.ID, 300, 2025, time.July, 17)
	f.spend(t, user, second.ID, 400, 2025, time.July, 17)

	stats := f.stats(t, user, "period=week&offset=1")

	assert.Equal(t, []int64{0, 700, 0, 700, 0, 0, 0}, amounts(stats.Series.Buckets), "a day adds up every category")
	assert.Equal(t, 1, stats.Series.PeakIndex)
}

func TestGetStatsSummary_EmptyUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.get(t, user, "/summary/stats?period=month")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"range": {"period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true},
		"total_spent_minor": 0, "previous_total_minor": 0, "delta_minor": 0, "delta_pct": null,
		"series": {
			"unit": "week",
			"buckets": [
				{"start": "2025-07-01", "end": "2025-07-07", "amount_minor": 0},
				{"start": "2025-07-08", "end": "2025-07-14", "amount_minor": 0},
				{"start": "2025-07-15", "end": "2025-07-21", "amount_minor": 0},
				{"start": "2025-07-22", "end": "2025-07-31", "amount_minor": 0}
			],
			"peak_index": 0
		},
		"breakdown": []
	}`, rec.Body.String())
}

func TestGetStatsSummary_OtherRowHasANullCategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for range 6 {
		category := testutil.NewCategory(t, f.pool, user.ID)
		f.spend(t, user, category.ID, 100, 2025, time.July, 2)
	}

	rec := f.get(t, user, "/summary/stats?period=month")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), `{"amount_minor":100,"category_id":null,"share_pct":16}`)
}

func TestGetStatsSummary_CountsEveryExpense(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	active := testutil.NewCategory(t, f.pool, user.ID)
	retired := testutil.NewCategory(t, f.pool, user.ID)
	f.spend(t, user, active.ID, 100, 2025, time.July, 2)
	f.spend(t, user, retired.ID, 300, 2025, time.July, 9)
	f.spend(t, user, retired.ID, 250, 2025, time.June, 9)
	f.earn(t, user, 99999, 2025, time.July, 2)
	archived := true
	_, err := db.New(f.pool).UpdateCategory(context.Background(), db.UpdateCategoryParams{ID: retired.ID, UserID: user.ID, Archived: &archived})
	require.NoError(t, err)

	stats := f.stats(t, user, "period=month")

	assert.EqualValues(t, 400, stats.TotalSpentMinor, "an archived category's spend counts; income does not")
	assert.EqualValues(t, 250, stats.PreviousTotalMinor)
	assert.Equal(t, pct(60), stats.DeltaPct)
	assert.Equal(t, []int64{100, 300, 0, 0}, amounts(stats.Series.Buckets))
	assert.Equal(t, []slice{{retired.ID, 300, 75}, {active.ID, 100, 25}}, slices(stats))
}

func TestGetStatsSummary_FollowsLocalDates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	timezone := "Asia/Yerevan" // UTC+4
	user = f.update(t, user, db.UpdateUserParams{Timezone: &timezone})
	category := testutil.NewCategory(t, f.pool, user.ID)
	// 21:00 UTC on 30 June is 1 July in Yerevan; 19:00 UTC is still 30 June.
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(700), testutil.TransactionAt(time.Date(2025, 6, 30, 21, 0, 0, 0, time.UTC)))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(400), testutil.TransactionAt(time.Date(2025, 6, 30, 19, 0, 0, 0, time.UTC)))

	stats := f.stats(t, user, "period=month")

	assert.EqualValues(t, 700, stats.TotalSpentMinor)
	assert.EqualValues(t, 400, stats.PreviousTotalMinor)
	assert.Equal(t, []int64{700, 0, 0, 0}, amounts(stats.Series.Buckets))
}

func TestGetStatsSummary_BadParameters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	for name, query := range map[string]string{
		"no period":           "",
		"unknown period":      "period=day",
		"negative offset":     "period=month&offset=-1",
		"offset out of reach": "period=month&offset=1201",
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.get(t, user, "/summary/stats?"+query)

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"bad_request"`)
		})
	}
}

func TestGetStatsSummary_ShowsOnlyTheCallersLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	theirs := testutil.NewCategory(t, f.pool, other.ID)
	f.spend(t, other, theirs.ID, 4200, 2025, time.July, 10)
	f.spend(t, other, theirs.ID, 1300, 2025, time.June, 10)

	stats := f.stats(t, owner, "period=month")

	assert.Zero(t, stats.TotalSpentMinor)
	assert.Zero(t, stats.PreviousTotalMinor)
	assert.Equal(t, []int64{0, 0, 0, 0}, amounts(stats.Series.Buckets))
	assert.Empty(t, stats.Breakdown)
}

func TestGetStatsSummary_RequiresAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/summary/stats?period=month", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}
