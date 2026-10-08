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

func (f fixture) categories(t *testing.T, user testutil.TestUser, query string) httpx.CategoriesSummary {
	t.Helper()
	rec := f.get(t, user, "/summary/categories?"+query)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.CategoriesSummary](t, rec)
}

func pct(n int) *int { return &n }

// item builds the expected figures of one category; remaining and the
// over-budget flag follow from the spend and the budget.
func item(categoryID uuid.UUID, spent, budget int64, usedPct *int, sharePct, txnCount int, trendPct *int) httpx.CategorySummaryItem {
	return httpx.CategorySummaryItem{
		CategoryID:     categoryID,
		SpentMinor:     spent,
		BudgetMinor:    budget,
		RemainingMinor: budget - spent,
		OverBudget:     budget > 0 && spent > budget,
		BudgetUsedPct:  usedPct,
		SharePct:       sharePct,
		TxnCount:       txnCount,
		TrendPct:       trendPct,
	}
}

func sharesTotal(summary httpx.CategoriesSummary) int {
	total := 0
	for _, i := range summary.Items {
		total += i.SharePct
	}
	return total
}

// design is the ledger of the design: seven budgeted categories, $1,842.50
// spent in July 2025 and $2,052.50 in June.
type design struct {
	food, transport, housing, utilities, leisure, selfCare, health uuid.UUID
}

func (f fixture) design(t *testing.T, user testutil.TestUser) design {
	t.Helper()
	category := func(name string, budget int64) uuid.UUID {
		return testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName(name), testutil.CategoryBudget(budget)).ID
	}
	// The budgets add up to $1,970.
	d := design{
		food:      category("Food", 50000),
		transport: category("Transport", 25000),
		housing:   category("Housing", 65000),
		utilities: category("Utilities", 15000),
		leisure:   category("Leisure", 15000),
		selfCare:  category("Self-care", 15000),
		health:    category("Health", 12000),
	}

	f.earn(t, user, 190000, 2025, time.June, 1)
	f.spend(t, user, d.housing, 60000, 2025, time.June, 1)
	f.spend(t, user, d.food, 52250, 2025, time.June, 12)
	f.spend(t, user, d.transport, 93000, 2025, time.June, 30)

	f.earn(t, user, 190355, 2025, time.July, 1)
	f.spend(t, user, d.housing, 60000, 2025, time.July, 1)
	f.spend(t, user, d.transport, 21000, 2025, time.July, 3)
	f.spend(t, user, d.utilities, 14500, 2025, time.July, 5)
	f.spend(t, user, d.leisure, 13500, 2025, time.July, 12)
	f.spend(t, user, d.selfCare, 16000, 2025, time.July, 14)
	f.spend(t, user, d.health, 11000, 2025, time.July, 18)
	f.spend(t, user, d.food, 45810, 2025, time.July, 20)
	f.spend(t, user, d.food, 2440, 2025, time.July, 21)
	return d
}

// TestGetCategoriesSummary_TheDesign reproduces the Categories screen of the
// design: Food at $482.50 of $500, Self-care $10 over its $150.
func TestGetCategoriesSummary_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	summary := f.categories(t, user, "period=month")

	assert.Equal(t, httpx.Period("month"), summary.Range.Period)
	assert.Equal(t, 0, summary.Range.Offset)
	assert.Equal(t, "2025-07-01", summary.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-31", summary.Range.End.Format(time.DateOnly))
	assert.True(t, summary.Range.IsCurrent)
	assert.EqualValues(t, 184250, summary.TotalSpentMinor)
	assert.EqualValues(t, 197000, summary.TotalBudgetMinor)
	assert.Equal(t, pct(94), summary.BudgetUsedPct)
	assert.Equal(t, []httpx.CategorySummaryItem{
		item(d.housing, 60000, 65000, pct(92), 33, 1, pct(0)),
		item(d.food, 48250, 50000, pct(97), 26, 2, pct(-8)),
		item(d.transport, 21000, 25000, pct(84), 11, 1, pct(-77)),
		item(d.selfCare, 16000, 15000, pct(107), 9, 1, nil),
		item(d.utilities, 14500, 15000, pct(97), 8, 1, nil),
		item(d.leisure, 13500, 15000, pct(90), 7, 1, nil),
		item(d.health, 11000, 12000, pct(92), 6, 1, nil),
	}, summary.Items)
	assert.Equal(t, 100, sharesTotal(summary))

	food, selfCare := summary.Items[1], summary.Items[3]
	assert.EqualValues(t, 1750, food.RemainingMinor)
	assert.False(t, food.OverBudget)
	assert.EqualValues(t, -1000, selfCare.RemainingMinor)
	assert.True(t, selfCare.OverBudget)
}

func TestGetCategoriesSummary_Week(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	summary := f.categories(t, user, "period=week")

	assert.Equal(t, "2025-07-21", summary.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-27", summary.Range.End.Format(time.DateOnly))
	assert.EqualValues(t, 2440, summary.TotalSpentMinor)
	assert.EqualValues(t, 45462, summary.TotalBudgetMinor, "the sum of the weekly budgets, each rounded")
	assert.Equal(t, pct(5), summary.BudgetUsedPct)
	// A weekly budget is round(monthly × 12 / 52). The week before had Food,
	// Self-care and Health spending.
	assert.Equal(t, []httpx.CategorySummaryItem{
		item(d.food, 2440, 11538, pct(21), 100, 1, pct(-95)),
		item(d.transport, 0, 5769, pct(0), 0, 0, nil),
		item(d.housing, 0, 15000, pct(0), 0, 0, nil),
		item(d.utilities, 0, 3462, pct(0), 0, 0, nil),
		item(d.leisure, 0, 3462, pct(0), 0, 0, nil),
		item(d.selfCare, 0, 3462, pct(0), 0, 0, pct(-100)),
		item(d.health, 0, 2769, pct(0), 0, 0, pct(-100)),
	}, summary.Items, "categories without spend follow in display order")
}

func TestGetCategoriesSummary_Year(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)
	f.spend(t, user, d.food, 50250, 2024, time.December, 31)

	summary := f.categories(t, user, "period=year")

	assert.Equal(t, "2025-01-01", summary.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-12-31", summary.Range.End.Format(time.DateOnly))
	assert.EqualValues(t, 389500, summary.TotalSpentMinor)
	assert.EqualValues(t, 197000*12, summary.TotalBudgetMinor)
	assert.Equal(t, pct(16), summary.BudgetUsedPct)
	assert.Equal(t, []httpx.CategorySummaryItem{
		item(d.housing, 120000, 780000, pct(15), 31, 2, nil),
		item(d.transport, 114000, 300000, pct(38), 29, 2, nil),
		item(d.food, 100500, 600000, pct(17), 26, 3, pct(100)),
		item(d.selfCare, 16000, 180000, pct(9), 4, 1, nil),
		item(d.utilities, 14500, 180000, pct(8), 4, 1, nil),
		item(d.leisure, 13500, 180000, pct(8), 3, 1, nil),
		item(d.health, 11000, 144000, pct(8), 3, 1, nil),
	}, summary.Items)
	assert.Equal(t, 100, sharesTotal(summary))
}

func TestGetCategoriesSummary_Offset(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	d := f.design(t, user)

	summary := f.categories(t, user, "period=month&offset=1")

	assert.Equal(t, 1, summary.Range.Offset)
	assert.Equal(t, "2025-06-01", summary.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-06-30", summary.Range.End.Format(time.DateOnly))
	assert.False(t, summary.Range.IsCurrent)
	assert.EqualValues(t, 205250, summary.TotalSpentMinor)
	require.Len(t, summary.Items, 7)
	assert.Equal(t, item(d.transport, 93000, 25000, pct(372), 45, 1, nil), summary.Items[0])
	assert.Equal(t, 100, sharesTotal(summary))
}

func TestGetCategoriesSummary_EmptyUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.get(t, user, "/summary/categories?period=month")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"range": {"period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true},
		"total_spent_minor": 0, "total_budget_minor": 0, "budget_used_pct": null,
		"items": []
	}`, rec.Body.String())
}

func TestGetCategoriesSummary_NullsArePresent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)

	rec := f.get(t, user, "/summary/categories?period=month")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"range": {"period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true},
		"total_spent_minor": 0, "total_budget_minor": 0, "budget_used_pct": null,
		"items": [{
			"category_id": "`+category.ID.String()+`", "spent_minor": 0, "budget_minor": 0, "remaining_minor": 0,
			"over_budget": false, "budget_used_pct": null, "share_pct": 0, "txn_count": 0, "trend_pct": null
		}]
	}`, rec.Body.String())
}

func TestGetCategoriesSummary_NoBudgetIsNeverOver(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	unbudgeted := testutil.NewCategory(t, f.pool, user.ID)
	exact := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(500))
	f.spend(t, user, unbudgeted.ID, 700, 2025, time.July, 2)
	f.spend(t, user, exact.ID, 500, 2025, time.July, 2)
	f.earn(t, user, 99999, 2025, time.July, 3)

	summary := f.categories(t, user, "period=month")

	assert.EqualValues(t, 1200, summary.TotalSpentMinor, "income is not spending")
	assert.EqualValues(t, 500, summary.TotalBudgetMinor)
	assert.Equal(t, pct(240), summary.BudgetUsedPct)
	require.Len(t, summary.Items, 2)
	assert.Equal(t, httpx.CategorySummaryItem{
		CategoryID: unbudgeted.ID, SpentMinor: 700, RemainingMinor: -700, OverBudget: false,
		BudgetUsedPct: nil, SharePct: 58, TxnCount: 1,
	}, summary.Items[0])
	assert.Equal(t, httpx.CategorySummaryItem{
		CategoryID: exact.ID, SpentMinor: 500, BudgetMinor: 500, RemainingMinor: 0, OverBudget: false,
		BudgetUsedPct: pct(100), SharePct: 42, TxnCount: 1,
	}, summary.Items[1], "spending the whole budget is not going over it")
}

func TestGetCategoriesSummary_ArchivedCategories(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	active := testutil.NewCategory(t, f.pool, user.ID)
	usedThisMonth := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(1000))
	usedLastMonth := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(1000))
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryArchived()) // never used
	f.spend(t, user, active.ID, 100, 2025, time.July, 2)
	f.spend(t, user, usedThisMonth.ID, 300, 2025, time.July, 3)
	f.spend(t, user, usedLastMonth.ID, 900, 2025, time.June, 3)
	archived := true
	for _, id := range []uuid.UUID{usedThisMonth.ID, usedLastMonth.ID} {
		_, err := db.New(f.pool).UpdateCategory(context.Background(), db.UpdateCategoryParams{ID: id, UserID: user.ID, Archived: &archived})
		require.NoError(t, err)
	}

	summary := f.categories(t, user, "period=month")

	assert.EqualValues(t, 400, summary.TotalSpentMinor)
	assert.EqualValues(t, 1000, summary.TotalBudgetMinor)
	assert.Equal(t, []httpx.CategorySummaryItem{
		item(usedThisMonth.ID, 300, 1000, pct(30), 75, 1, nil),
		item(active.ID, 100, 0, nil, 25, 1, nil),
	}, summary.Items, "an archived category shows only in a period it has spend in")
}

func TestGetCategoriesSummary_EqualSpendComesInDisplayOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	first := testutil.NewCategory(t, f.pool, user.ID)
	second := testutil.NewCategory(t, f.pool, user.ID)
	third := testutil.NewCategory(t, f.pool, user.ID)
	f.spend(t, user, third.ID, 100, 2025, time.July, 2)
	f.spend(t, user, second.ID, 100, 2025, time.July, 3)
	f.spend(t, user, first.ID, 100, 2025, time.July, 4)

	summary := f.categories(t, user, "period=month")

	assert.Equal(t, []httpx.CategorySummaryItem{
		item(first.ID, 100, 0, nil, 34, 1, nil),
		item(second.ID, 100, 0, nil, 33, 1, nil),
		item(third.ID, 100, 0, nil, 33, 1, nil),
	}, summary.Items)
}

func TestGetCategoriesSummary_FollowsLocalDates(t *testing.T) {
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

	summary := f.categories(t, user, "period=month")

	assert.Equal(t, []httpx.CategorySummaryItem{item(category.ID, 700, 0, nil, 100, 1, pct(75))}, summary.Items)
}

func TestGetCategoriesSummary_BadParameters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	for name, query := range map[string]string{
		"no period":           "",
		"unknown period":      "period=day",
		"negative offset":     "period=month&offset=-1",
		"offset out of reach": "period=month&offset=1201",
		"offset not a number": "period=month&offset=last",
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.get(t, user, "/summary/categories?"+query)

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"bad_request"`)
		})
	}

	rec := f.get(t, user, "/summary/categories?period=year&offset=1200")
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

func TestGetCategoriesSummary_ShowsOnlyTheCallersLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	mine := testutil.NewCategory(t, f.pool, owner.ID, testutil.CategoryBudget(1000))
	theirs := testutil.NewCategory(t, f.pool, other.ID, testutil.CategoryBudget(9000))
	f.spend(t, other, theirs.ID, 4200, 2025, time.July, 10)
	f.spend(t, other, theirs.ID, 1300, 2025, time.June, 10)

	summary := f.categories(t, owner, "period=month")

	assert.Zero(t, summary.TotalSpentMinor)
	assert.EqualValues(t, 1000, summary.TotalBudgetMinor)
	assert.Equal(t, []httpx.CategorySummaryItem{item(mine.ID, 0, 1000, pct(0), 0, 0, nil)}, summary.Items)
}

func TestGetCategoriesSummary_RequiresAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/summary/categories?period=month", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}
