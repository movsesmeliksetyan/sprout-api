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

func (f fixture) category(t *testing.T, user testutil.TestUser, id uuid.UUID, query string) httpx.CategorySummary {
	t.Helper()
	rec := f.get(t, user, "/summary/categories/"+id.String()+"?"+query)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.CategorySummary](t, rec)
}

// amounts lists what each bucket holds.
func amounts(buckets []httpx.Bucket) []int64 {
	out := make([]int64, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, b.AmountMinor)
	}
	return out
}

// spans lists the first and last day of each bucket, as "start/end".
func spans(buckets []httpx.Bucket) []string {
	out := make([]string, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, b.Start.Format(time.DateOnly)+"/"+b.End.Format(time.DateOnly))
	}
	return out
}

// TestGetCategorySummary_TheDesign reads Food on the design's ledger: $482.50
// of $500 in July, all of it in the third week.
func TestGetCategorySummary_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	food := f.category(t, user, d.food, "period=month")

	assert.Equal(t, "2025-07-01", food.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-31", food.Range.End.Format(time.DateOnly))
	assert.True(t, food.Range.IsCurrent)
	assert.Equal(t, d.food, food.CategoryID)
	assert.EqualValues(t, 48250, food.SpentMinor)
	assert.EqualValues(t, 50000, food.BudgetMinor)
	assert.EqualValues(t, 1750, food.RemainingMinor)
	assert.False(t, food.OverBudget)
	assert.Equal(t, pct(97), food.BudgetUsedPct)
	assert.Equal(t, 2, food.TxnCount)
	assert.Equal(t, httpx.BucketUnit("week"), food.Trend.Unit)
	assert.Equal(t, []string{
		"2025-07-01/2025-07-07", "2025-07-08/2025-07-14", "2025-07-15/2025-07-21", "2025-07-22/2025-07-31",
	}, spans(food.Trend.Buckets))
	assert.Equal(t, []int64{0, 0, 48250, 0}, amounts(food.Trend.Buckets), "the week still to come holds 0")
	assert.EqualValues(t, 16083, food.Trend.AverageMinor, "over the three weeks that have started")
	assert.EqualValues(t, 48250, food.Trend.PeakMinor)
	assert.Equal(t, 2, food.Trend.PeakIndex)

	selfCare := f.category(t, user, d.selfCare, "period=month")

	assert.EqualValues(t, 16000, selfCare.SpentMinor)
	assert.EqualValues(t, -1000, selfCare.RemainingMinor)
	assert.True(t, selfCare.OverBudget)
	assert.Equal(t, pct(107), selfCare.BudgetUsedPct)
	assert.Equal(t, []int64{0, 16000, 0, 0}, amounts(selfCare.Trend.Buckets))
}

func TestGetCategorySummary_PastMonth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	june := f.category(t, user, d.food, "period=month&offset=1")

	assert.False(t, june.Range.IsCurrent)
	assert.Equal(t, 1, june.Range.Offset)
	assert.Equal(t, []string{
		"2025-06-01/2025-06-07", "2025-06-08/2025-06-14", "2025-06-15/2025-06-21", "2025-06-22/2025-06-30",
	}, spans(june.Trend.Buckets))
	assert.Equal(t, []int64{0, 52250, 0, 0}, amounts(june.Trend.Buckets))
	assert.EqualValues(t, 52250, june.SpentMinor)
	assert.True(t, june.OverBudget)
	assert.EqualValues(t, 13063, june.Trend.AverageMinor, "over all four weeks of a month that has ended")
	assert.Equal(t, 1, june.Trend.PeakIndex)

	february := f.category(t, user, d.food, "period=month&offset=5")

	assert.Equal(t, []string{
		"2025-02-01/2025-02-07", "2025-02-08/2025-02-14", "2025-02-15/2025-02-21", "2025-02-22/2025-02-28",
	}, spans(february.Trend.Buckets))
}

func TestGetCategorySummary_Week(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(5200))
	// The week before: Tuesday and Thursday tie for the peak.
	f.spend(t, user, category.ID, 700, 2025, time.July, 15)
	f.spend(t, user, category.ID, 400, 2025, time.July, 17)
	f.spend(t, user, category.ID, 300, 2025, time.July, 17)
	f.spend(t, user, category.ID, 100, 2025, time.July, 20)
	// This week: today, a Monday.
	f.spend(t, user, category.ID, 300, 2025, time.July, 21)
	f.spend(t, user, category.ID, 200, 2025, time.July, 21)

	current := f.category(t, user, category.ID, "period=week")

	assert.Equal(t, httpx.BucketUnit("day"), current.Trend.Unit)
	assert.Equal(t, []string{
		"2025-07-21/2025-07-21", "2025-07-22/2025-07-22", "2025-07-23/2025-07-23", "2025-07-24/2025-07-24",
		"2025-07-25/2025-07-25", "2025-07-26/2025-07-26", "2025-07-27/2025-07-27",
	}, spans(current.Trend.Buckets))
	assert.Equal(t, []int64{500, 0, 0, 0, 0, 0, 0}, amounts(current.Trend.Buckets))
	assert.EqualValues(t, 500, current.SpentMinor)
	assert.EqualValues(t, 1200, current.BudgetMinor, "round(monthly × 12 / 52)")
	assert.EqualValues(t, 700, current.RemainingMinor)
	assert.Equal(t, pct(42), current.BudgetUsedPct)
	assert.Equal(t, 2, current.TxnCount)
	assert.EqualValues(t, 500, current.Trend.AverageMinor, "only Monday has started")
	assert.EqualValues(t, 500, current.Trend.PeakMinor)
	assert.Equal(t, 0, current.Trend.PeakIndex)

	previous := f.category(t, user, category.ID, "period=week&offset=1")

	assert.Equal(t, "2025-07-14", previous.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-20", previous.Range.End.Format(time.DateOnly))
	assert.Equal(t, []int64{0, 700, 0, 700, 0, 0, 100}, amounts(previous.Trend.Buckets))
	assert.EqualValues(t, 1500, previous.SpentMinor)
	assert.True(t, previous.OverBudget)
	assert.Equal(t, 4, previous.TxnCount)
	assert.EqualValues(t, 214, previous.Trend.AverageMinor)
	assert.EqualValues(t, 700, previous.Trend.PeakMinor)
	assert.Equal(t, 1, previous.Trend.PeakIndex, "the first of two equal days")
}

func TestGetCategorySummary_Year(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)
	f.spend(t, user, d.food, 1200, 2024, time.February, 29)

	current := f.category(t, user, d.food, "period=year")

	assert.Equal(t, httpx.BucketUnit("month"), current.Trend.Unit)
	require.Len(t, current.Trend.Buckets, 12)
	assert.Equal(t, "2025-01-01/2025-01-31", spans(current.Trend.Buckets)[0])
	assert.Equal(t, "2025-02-01/2025-02-28", spans(current.Trend.Buckets)[1])
	assert.Equal(t, "2025-12-01/2025-12-31", spans(current.Trend.Buckets)[11])
	assert.Equal(t, []int64{0, 0, 0, 0, 0, 52250, 48250, 0, 0, 0, 0, 0}, amounts(current.Trend.Buckets))
	assert.EqualValues(t, 100500, current.SpentMinor)
	assert.EqualValues(t, 600000, current.BudgetMinor)
	assert.Equal(t, pct(17), current.BudgetUsedPct)
	assert.Equal(t, 3, current.TxnCount)
	assert.EqualValues(t, 14357, current.Trend.AverageMinor, "over January to July")
	assert.EqualValues(t, 52250, current.Trend.PeakMinor)
	assert.Equal(t, 5, current.Trend.PeakIndex)

	previous := f.category(t, user, d.food, "period=year&offset=1")

	assert.Equal(t, "2024-02-01/2024-02-29", spans(previous.Trend.Buckets)[1])
	assert.Equal(t, []int64{0, 1200, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, amounts(previous.Trend.Buckets))
	assert.EqualValues(t, 100, previous.Trend.AverageMinor)
	assert.Equal(t, 1, previous.Trend.PeakIndex)
}

func TestGetCategorySummary_NoSpend(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)

	rec := f.get(t, user, "/summary/categories/"+category.ID.String()+"?period=month")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"range": {"period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true},
		"category_id": "`+category.ID.String()+`",
		"spent_minor": 0, "budget_minor": 0, "remaining_minor": 0, "over_budget": false,
		"budget_used_pct": null, "txn_count": 0,
		"trend": {
			"unit": "week",
			"buckets": [
				{"start": "2025-07-01", "end": "2025-07-07", "amount_minor": 0},
				{"start": "2025-07-08", "end": "2025-07-14", "amount_minor": 0},
				{"start": "2025-07-15", "end": "2025-07-21", "amount_minor": 0},
				{"start": "2025-07-22", "end": "2025-07-31", "amount_minor": 0}
			],
			"average_minor": 0, "peak_minor": 0, "peak_index": 0
		}
	}`, rec.Body.String())
}

func TestGetCategorySummary_CountsOnlyTheCategorysExpenses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	another := testutil.NewCategory(t, f.pool, user.ID)
	f.spend(t, user, category.ID, 700, 2025, time.July, 2)
	f.spend(t, user, another.ID, 9000, 2025, time.July, 2)
	f.earn(t, user, 99999, 2025, time.July, 2)
	f.spend(t, user, category.ID, 50, 2025, time.June, 30)
	f.spend(t, user, category.ID, 60, 2025, time.August, 1)

	summary := f.category(t, user, category.ID, "period=month")

	assert.EqualValues(t, 700, summary.SpentMinor)
	assert.Equal(t, 1, summary.TxnCount)
	assert.EqualValues(t, -700, summary.RemainingMinor)
	assert.False(t, summary.OverBudget, "a category without a budget is never over it")
	assert.Nil(t, summary.BudgetUsedPct)
	assert.Equal(t, []int64{700, 0, 0, 0}, amounts(summary.Trend.Buckets))
}

func TestGetCategorySummary_FollowsLocalDates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	timezone := "Asia/Yerevan" // UTC+4
	user = f.update(t, user, db.UpdateUserParams{Timezone: &timezone})
	category := testutil.NewCategory(t, f.pool, user.ID)
	// 21:00 UTC on 7 July is the 8th in Yerevan; 19:00 UTC is still the 7th.
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(700), testutil.TransactionAt(time.Date(2025, 7, 7, 21, 0, 0, 0, time.UTC)))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(400), testutil.TransactionAt(time.Date(2025, 7, 7, 19, 0, 0, 0, time.UTC)))

	summary := f.category(t, user, category.ID, "period=month")

	assert.Equal(t, []int64{400, 700, 0, 0}, amounts(summary.Trend.Buckets))
}

func TestGetCategorySummary_ArchivedCategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(1000))
	f.spend(t, user, category.ID, 300, 2025, time.July, 3)
	archived := true
	_, err := db.New(f.pool).UpdateCategory(context.Background(), db.UpdateCategoryParams{ID: category.ID, UserID: user.ID, Archived: &archived})
	require.NoError(t, err)

	summary := f.category(t, user, category.ID, "period=month")

	assert.EqualValues(t, 300, summary.SpentMinor)
	assert.EqualValues(t, 1000, summary.BudgetMinor)
	assert.Equal(t, []int64{300, 0, 0, 0}, amounts(summary.Trend.Buckets))
}

func TestGetCategorySummary_NotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	theirs := testutil.NewCategory(t, f.pool, other.ID)
	f.spend(t, other, theirs.ID, 4200, 2025, time.July, 10)
	unknown, err := uuid.NewV7()
	require.NoError(t, err)

	for name, id := range map[string]uuid.UUID{"unknown category": unknown, "someone else's category": theirs.ID} {
		t.Run(name, func(t *testing.T) {
			rec := f.get(t, owner, "/summary/categories/"+id.String()+"?period=month")

			require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"not_found"`)
		})
	}
}

func TestGetCategorySummary_BadParameters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	path := "/summary/categories/" + category.ID.String()

	for name, target := range map[string]string{
		"id is not a uuid":    "/summary/categories/food?period=month",
		"no period":           path,
		"unknown period":      path + "?period=day",
		"negative offset":     path + "?period=month&offset=-1",
		"offset out of reach": path + "?period=month&offset=1201",
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.get(t, user, target)

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"bad_request"`)
		})
	}
}

func TestGetCategorySummary_RequiresAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, err := uuid.NewV7()
	require.NoError(t, err)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/summary/categories/"+id.String()+"?period=month", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}
