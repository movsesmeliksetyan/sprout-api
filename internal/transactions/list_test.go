package transactions_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// list returns one page of the user's transactions; query is the query
// string without its question mark.
func (f fixture) list(t *testing.T, user testutil.TestUser, query string) httpx.TransactionList {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/transactions?"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.TransactionList](t, rec)
}

// noon is midday UTC on a day of July 2025, the time the list tests file
// their transactions at.
func noon(day int) time.Time {
	return time.Date(2025, 7, day, 12, 0, 0, 0, time.UTC)
}

func ids(items []httpx.Transaction) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

// day is a DayTotal in a form that compares and prints well.
type day struct {
	Date     string
	NetMinor int64
	Count    int
}

func days(list httpx.TransactionList) []day {
	out := make([]day, 0, len(list.Days))
	for _, d := range list.Days {
		out = append(out, day{d.Date.Format(time.DateOnly), d.NetMinor, d.Count})
	}
	return out
}

func TestListTransactions_Empty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodGet, "/transactions", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{"items": [], "days": [], "next_cursor": null}`, rec.Body.String())
}

func TestListTransactions_PagesHoldEveryRowOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	const rows, limit = 500, 50
	seeded := make(map[uuid.UUID]bool, rows)
	for i := range rows {
		// 13 rows a day, so that days straddle page boundaries.
		opts := []testutil.TransactionOption{testutil.TransactionAt(noon(1).AddDate(0, 0, i/13))}
		if i%3 != 0 {
			opts = append(opts, testutil.TransactionExpense(category.ID))
		}
		seeded[testutil.NewTransaction(t, f.pool, user.User, opts...).ID] = true
	}

	var (
		all    []httpx.Transaction
		pages  int
		cursor string
	)
	for {
		page := f.list(t, user, fmt.Sprintf("limit=%d&cursor=%s", limit, cursor))
		pages++
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			break
		}
		require.Len(t, page.Items, limit, "a page before the last is full")
		require.LessOrEqual(t, pages, rows, "paging does not end")
		cursor = *page.NextCursor
	}

	assert.Equal(t, rows/limit, pages)
	require.Len(t, all, rows)
	for i, item := range all {
		assert.True(t, seeded[item.ID], "row %d was seeded and not seen before", i)
		delete(seeded, item.ID)
		if i == 0 {
			continue
		}
		previous := all[i-1]
		newer := previous.LocalDate.After(item.LocalDate.Time) ||
			(previous.LocalDate.Equal(item.LocalDate.Time) && strings.Compare(previous.ID.String(), item.ID.String()) > 0)
		assert.True(t, newer, "row %d comes after row %d", i, i-1)
	}
	assert.Empty(t, seeded, "every row was listed")
}

func TestListTransactions_DefaultLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for range 51 {
		testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(1)))
	}

	first := f.list(t, user, "")
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.NextCursor)

	second := f.list(t, user, "cursor="+*first.NextCursor)
	assert.Len(t, second.Items, 1)
	assert.Nil(t, second.NextCursor)

	exact := f.list(t, user, "limit=51")
	assert.Len(t, exact.Items, 51)
	assert.Nil(t, exact.NextCursor, "a page that ends the list has no cursor")
}

func TestListTransactions_DaySplitAcrossPagesKeepsItsFullTotal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	// The 21st: 5000 in, 2440 and 1060 out. The 20th: 300 out.
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(21)), testutil.TransactionAmount(5000))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(21)), testutil.TransactionAmount(2440), testutil.TransactionExpense(category.ID))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(21)), testutil.TransactionAmount(1060), testutil.TransactionExpense(category.ID))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(20)), testutil.TransactionAmount(300), testutil.TransactionExpense(category.ID))

	first := f.list(t, user, "limit=2")
	require.NotNil(t, first.NextCursor)
	second := f.list(t, user, "limit=2&cursor="+*first.NextCursor)

	whole := day{"2025-07-21", 1500, 3}
	assert.Equal(t, []day{whole}, days(first), "two of the day's three rows are on this page")
	assert.Equal(t, []day{whole, {"2025-07-20", -300, 1}}, days(second), "the third is on this one")
	assert.Nil(t, second.NextCursor)
}

func TestListTransactions_Filters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID)
	transport := testutil.NewCategory(t, f.pool, user.ID)
	seed := func(dayOfMonth int, amount int64, opts ...testutil.TransactionOption) uuid.UUID {
		opts = append(opts, testutil.TransactionAt(noon(dayOfMonth)), testutil.TransactionAmount(amount))
		return testutil.NewTransaction(t, f.pool, user.User, opts...).ID
	}
	salary := seed(19, 150000, testutil.TransactionMerchant("Employer"), testutil.TransactionNote("July salary"))
	tesco := seed(20, 2440, testutil.TransactionExpense(food.ID), testutil.TransactionMerchant("Tesco"), testutil.TransactionNote("Groceries"))
	coffee := seed(21, 350, testutil.TransactionExpense(food.ID), testutil.TransactionMerchant("Blue Bottle Coffee"))
	bus := seed(21, 180, testutil.TransactionExpense(transport.ID), testutil.TransactionNote("Bus to the COFFEE shop"))
	discount := seed(22, 900, testutil.TransactionExpense(food.ID), testutil.TransactionNote("50% off_peak deal"))
	plain := seed(22, 500)

	tests := []struct {
		name  string
		query url.Values
		items []uuid.UUID
		days  []day
	}{
		{"no filter", url.Values{},
			[]uuid.UUID{plain, discount, bus, coffee, tesco, salary},
			[]day{{"2025-07-22", -400, 2}, {"2025-07-21", -530, 2}, {"2025-07-20", -2440, 1}, {"2025-07-19", 150000, 1}}},
		{"from", url.Values{"from": {"2025-07-22"}},
			[]uuid.UUID{plain, discount}, []day{{"2025-07-22", -400, 2}}},
		{"to", url.Values{"to": {"2025-07-19"}},
			[]uuid.UUID{salary}, []day{{"2025-07-19", 150000, 1}}},
		{"from and to are inclusive", url.Values{"from": {"2025-07-20"}, "to": {"2025-07-21"}},
			[]uuid.UUID{bus, coffee, tesco}, []day{{"2025-07-21", -530, 2}, {"2025-07-20", -2440, 1}}},
		{"from after to", url.Values{"from": {"2025-07-22"}, "to": {"2025-07-20"}},
			[]uuid.UUID{}, []day{}},
		{"category", url.Values{"category_id": {food.ID.String()}},
			[]uuid.UUID{discount, coffee, tesco},
			[]day{{"2025-07-22", -900, 1}, {"2025-07-21", -350, 1}, {"2025-07-20", -2440, 1}}},
		{"unknown category", url.Values{"category_id": {uuid.NewString()}},
			[]uuid.UUID{}, []day{}},
		{"kind income", url.Values{"kind": {"income"}},
			[]uuid.UUID{plain, salary}, []day{{"2025-07-22", 500, 1}, {"2025-07-19", 150000, 1}}},
		{"kind expense", url.Values{"kind": {"expense"}},
			[]uuid.UUID{discount, bus, coffee, tesco},
			[]day{{"2025-07-22", -900, 1}, {"2025-07-21", -530, 2}, {"2025-07-20", -2440, 1}}},
		{"q in merchant or note, any case", url.Values{"q": {"coffee"}},
			[]uuid.UUID{bus, coffee}, []day{{"2025-07-21", -530, 2}}},
		{"q is trimmed", url.Values{"q": {"  tesco "}},
			[]uuid.UUID{tesco}, []day{{"2025-07-20", -2440, 1}}},
		{"blank q", url.Values{"q": {"   "}, "to": {"2025-07-19"}},
			[]uuid.UUID{salary}, []day{{"2025-07-19", 150000, 1}}},
		{"q with a percent sign", url.Values{"q": {"50% off"}},
			[]uuid.UUID{discount}, []day{{"2025-07-22", -900, 1}}},
		{"q percent matches nothing else", url.Values{"q": {"%"}},
			[]uuid.UUID{discount}, []day{{"2025-07-22", -900, 1}}},
		{"q underscore is literal", url.Values{"q": {"_"}},
			[]uuid.UUID{discount}, []day{{"2025-07-22", -900, 1}}},
		{"q with no match", url.Values{"q": {"zebra"}},
			[]uuid.UUID{}, []day{}},
		{"combined", url.Values{"category_id": {food.ID.String()}, "kind": {"expense"}, "from": {"2025-07-21"}, "q": {"o"}},
			[]uuid.UUID{discount, coffee}, []day{{"2025-07-22", -900, 1}, {"2025-07-21", -350, 1}}},
	}
	for _, tt := range tests {
		page := f.list(t, user, tt.query.Encode())

		assert.Equal(t, tt.items, ids(page.Items), tt.name)
		assert.Equal(t, tt.days, days(page), tt.name)
		assert.Nil(t, page.NextCursor, tt.name)
	}
}

func TestListTransactions_FiltersHoldAcrossPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	var expenses []uuid.UUID
	for i := range 6 {
		testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(21)), testutil.TransactionAmount(1000))
		expense := testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionAt(noon(21)),
			testutil.TransactionAmount(int64(i+1)), testutil.TransactionExpense(category.ID))
		expenses = append([]uuid.UUID{expense.ID}, expenses...)
	}

	first := f.list(t, user, "kind=expense&limit=4")
	require.NotNil(t, first.NextCursor)
	second := f.list(t, user, "kind=expense&limit=4&cursor="+*first.NextCursor)

	assert.Equal(t, expenses, append(ids(first.Items), ids(second.Items)...))
	assert.Equal(t, []day{{"2025-07-21", -21, 6}}, days(first))
	assert.Equal(t, []day{{"2025-07-21", -21, 6}}, days(second))
	assert.Nil(t, second.NextCursor)
}

func TestListTransactions_BadRequests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	testutil.NewTransaction(t, f.pool, user.User)

	queries := map[string]string{
		"limit of zero":        "limit=0",
		"negative limit":       "limit=-1",
		"limit over the max":   "limit=201",
		"limit not a number":   "limit=many",
		"unknown kind":         "kind=transfer",
		"from not a date":      "from=yesterday",
		"to not a date":        "to=2025-13-01",
		"category not a uuid":  "category_id=food",
		"cursor not a cursor":  "cursor=abc",
		"cursor tampered with": "cursor=" + httpx.EncodeCursor(httpx.Cursor{LocalDate: noon(1), ID: uuid.New()}) + "A",
		"q too long":           "q=" + strings.Repeat("a", 101),
	}
	for name, query := range queries {
		rec := f.do(t, user, http.MethodGet, "/transactions?"+query, nil)

		require.Equal(t, http.StatusBadRequest, rec.Code, "%s; body: %s", name, rec.Body.String())
		assert.Equal(t, "bad_request", testutil.DecodeJSON[errorBody](t, rec).Error.Code, name)
	}

	assert.Len(t, f.list(t, user, "limit=200&q="+strings.Repeat("a", 100)).Items, 0, "the limits themselves are allowed")
}

func TestListTransactions_ShowsOnlyTheCallersTransactions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	othersCategory := testutil.NewCategory(t, f.pool, other.ID)
	mine := testutil.NewTransaction(t, f.pool, owner.User, testutil.TransactionAt(noon(21)), testutil.TransactionAmount(700))
	testutil.NewTransaction(t, f.pool, other.User, testutil.TransactionAt(noon(21)),
		testutil.TransactionAmount(9900), testutil.TransactionExpense(othersCategory.ID), testutil.TransactionMerchant("Tesco"))

	page := f.list(t, owner, "")
	assert.Equal(t, []uuid.UUID{mine.ID}, ids(page.Items))
	assert.Equal(t, []day{{"2025-07-21", 700, 1}}, days(page), "the day's total leaves the other user out")

	assert.Empty(t, f.list(t, owner, "category_id="+othersCategory.ID.String()).Items)
	assert.Empty(t, f.list(t, owner, "q=tesco").Items)
}

func TestListTransactions_RequiresAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/transactions", nil))

	requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
}
