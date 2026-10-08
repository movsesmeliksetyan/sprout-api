package summary_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// now is the moment the tests look at the ledger from: a Monday afternoon
// in July 2025.
var now = time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)

// api is the server's API with only the summary handler implemented.
type api struct {
	*summary.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

// fixture is a server with a real Postgres behind it, its clock set to now.
type fixture struct {
	pool   *pgxpool.Pool
	server http.Handler
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	service := summary.NewService(pool, transactions.NewService(pool), summary.WithClock(func() time.Time { return now }))
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: summary.NewHandler(service)}),
	).Handler()
	return fixture{pool: pool, server: server}
}

func (f fixture) get(t *testing.T, user testutil.TestUser, path string) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1"+path, nil))
}

func (f fixture) home(t *testing.T, user testutil.TestUser) httpx.Home {
	t.Helper()
	rec := f.get(t, user, "/home")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Home](t, rec)
}

// update changes the user's profile and returns the user as stored.
func (f fixture) update(t *testing.T, user testutil.TestUser, params db.UpdateUserParams) testutil.TestUser {
	t.Helper()
	params.ID = user.ID
	updated, err := db.New(f.pool).UpdateUser(context.Background(), params)
	require.NoError(t, err)
	user.User = updated
	return user
}

// spend files an expense of amount under the category at midday UTC on the
// given day.
func (f fixture) spend(t *testing.T, user testutil.TestUser, categoryID uuid.UUID, amount int64, year int, month time.Month, day int) db.Transaction {
	t.Helper()
	return testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(categoryID),
		testutil.TransactionAmount(amount), testutil.TransactionAt(time.Date(year, month, day, 12, 0, 0, 0, time.UTC)))
}

// earn files income of amount at midday UTC on the given day.
func (f fixture) earn(t *testing.T, user testutil.TestUser, amount int64, year int, month time.Month, day int) db.Transaction {
	t.Helper()
	return testutil.NewTransaction(t, f.pool, user.User,
		testutil.TransactionAmount(amount), testutil.TransactionAt(time.Date(year, month, day, 12, 0, 0, 0, time.UTC)))
}

// segment is a HomeSegment in a form that compares and prints well.
type segment struct {
	CategoryID uuid.UUID
	SpentMinor int64
	Share      float64
}

func segments(home httpx.Home) []segment {
	out := make([]segment, 0, len(home.Month.Segments))
	for _, s := range home.Month.Segments {
		out = append(out, segment{s.CategoryID, s.SpentMinor, s.Share})
	}
	return out
}

func TestGetHome_EmptyUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.get(t, user, "/home")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{
		"balance_minor": 0,
		"month": {
			"range": {"period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true},
			"spent_minor": 0, "previous_spent_minor": 0, "delta_minor": 0,
			"categories_count": 0,
			"segments": []
		},
		"recent": {"items": [], "days": []},
		"has_unread_notifications": false
	}`, rec.Body.String())
}

// TestGetHome_TheDesign reproduces the Home screen of the design: a balance
// of $408.55 and $1,842.50 spent in July across seven categories, $210 less
// than in June.
func TestGetHome_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	startingBalance := int64(50000)
	user = f.update(t, user, db.UpdateUserParams{StartingBalanceMinor: &startingBalance})

	category := func(name string) uuid.UUID {
		return testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName(name)).ID
	}
	food, transport, housing := category("Food"), category("Transport"), category("Housing")
	utilities, leisure, selfCare, health := category("Utilities"), category("Leisure"), category("Self-care"), category("Health")

	// June: $2,052.50 spent.
	f.earn(t, user, 190000, 2025, time.June, 1)
	f.spend(t, user, housing, 60000, 2025, time.June, 1)
	f.spend(t, user, food, 52250, 2025, time.June, 12)
	f.spend(t, user, transport, 93000, 2025, time.June, 30)
	// July: $1,842.50 spent, Food in two visits.
	f.earn(t, user, 190355, 2025, time.July, 1)
	f.spend(t, user, housing, 60000, 2025, time.July, 1)
	f.spend(t, user, transport, 21000, 2025, time.July, 3)
	f.spend(t, user, utilities, 14500, 2025, time.July, 5)
	f.spend(t, user, leisure, 13500, 2025, time.July, 12)
	f.spend(t, user, selfCare, 16000, 2025, time.July, 14)
	f.spend(t, user, health, 11000, 2025, time.July, 18)
	f.spend(t, user, food, 45810, 2025, time.July, 20)
	tesco := f.spend(t, user, food, 2440, 2025, time.July, 21)

	home := f.home(t, user)

	assert.EqualValues(t, 40855, home.BalanceMinor)
	assert.EqualValues(t, 184250, home.Month.SpentMinor)
	assert.EqualValues(t, 205250, home.Month.PreviousSpentMinor)
	assert.EqualValues(t, -21000, home.Month.DeltaMinor)
	assert.Equal(t, 7, home.Month.CategoriesCount)
	assert.Equal(t, "2025-07-01", home.Month.Range.Start.Format(time.DateOnly))
	assert.Equal(t, "2025-07-31", home.Month.Range.End.Format(time.DateOnly))
	assert.True(t, home.Month.Range.IsCurrent)
	assert.Equal(t, []segment{
		{housing, 60000, 0.33},
		{food, 48250, 0.26},
		{transport, 21000, 0.11},
		{selfCare, 16000, 0.09},
		{utilities, 14500, 0.08},
		{leisure, 13500, 0.07},
		{health, 11000, 0.06},
	}, segments(home))
	total := 0
	for _, s := range home.Month.Segments {
		total += int(s.Share*100 + 0.5)
	}
	assert.Equal(t, 100, total, "the shares add up to 1")

	require.Len(t, home.Recent.Items, 10, "the ten newest of thirteen")
	assert.Equal(t, tesco.ID, home.Recent.Items[0].ID)
	assert.Equal(t, "2025-06-30", home.Recent.Items[9].LocalDate.Format(time.DateOnly))
	require.Len(t, home.Recent.Days, 9, "two of the ten are on the 1st of July")
	assert.Equal(t, "2025-07-21", home.Recent.Days[0].Date.Format(time.DateOnly))
	assert.EqualValues(t, -2440, home.Recent.Days[0].NetMinor)
	july1 := home.Recent.Days[7]
	assert.Equal(t, "2025-07-01", july1.Date.Format(time.DateOnly))
	assert.EqualValues(t, 190355-60000, july1.NetMinor)
	assert.Equal(t, 2, july1.Count)
	assert.False(t, home.HasUnreadNotifications)
}

func TestGetHome_BalanceCanBeNegative(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	startingBalance := int64(-1000)
	user = f.update(t, user, db.UpdateUserParams{StartingBalanceMinor: &startingBalance})
	category := testutil.NewCategory(t, f.pool, user.ID)
	f.earn(t, user, 500, 2024, time.January, 1)
	f.spend(t, user, category.ID, 2500, 2024, time.January, 2)

	home := f.home(t, user)

	assert.EqualValues(t, -3000, home.BalanceMinor, "every transaction counts, however old")
	assert.Zero(t, home.Month.SpentMinor)
	assert.Empty(t, home.Month.Segments)
}

func TestGetHome_MonthFollowsLocalDates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	timezone := "Asia/Yerevan" // UTC+4
	user = f.update(t, user, db.UpdateUserParams{Timezone: &timezone})
	category := testutil.NewCategory(t, f.pool, user.ID)
	// 21:00 UTC on 30 June is 1 July in Yerevan; 21:00 UTC on 31 May is 1 June.
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(700), testutil.TransactionAt(time.Date(2025, 6, 30, 21, 0, 0, 0, time.UTC)))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(300), testutil.TransactionAt(time.Date(2025, 5, 31, 21, 0, 0, 0, time.UTC)))
	testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(50), testutil.TransactionAt(time.Date(2025, 5, 31, 19, 0, 0, 0, time.UTC)))

	home := f.home(t, user)

	assert.EqualValues(t, 700, home.Month.SpentMinor)
	assert.EqualValues(t, 300, home.Month.PreviousSpentMinor)
	assert.EqualValues(t, 400, home.Month.DeltaMinor)
	assert.Equal(t, []segment{{category.ID, 700, 1}}, segments(home))
}

func TestGetHome_Segments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	first := testutil.NewCategory(t, f.pool, user.ID)
	second := testutil.NewCategory(t, f.pool, user.ID)
	third := testutil.NewCategory(t, f.pool, user.ID)
	testutil.NewCategory(t, f.pool, user.ID) // never used
	f.spend(t, user, third.ID, 100, 2025, time.July, 2)
	f.spend(t, user, second.ID, 100, 2025, time.July, 3)
	f.spend(t, user, first.ID, 100, 2025, time.July, 4)
	f.earn(t, user, 99999, 2025, time.July, 5)
	// Archived after it was used: its spending is still this month's.
	archived := true
	_, err := db.New(f.pool).UpdateCategory(context.Background(), db.UpdateCategoryParams{ID: third.ID, UserID: user.ID, Archived: &archived})
	require.NoError(t, err)

	home := f.home(t, user)

	assert.EqualValues(t, 300, home.Month.SpentMinor, "income is not spending")
	assert.Equal(t, []segment{{first.ID, 100, 0.34}, {second.ID, 100, 0.33}, {third.ID, 100, 0.33}}, segments(home),
		"equal amounts come in display order, and the odd percent goes to the first")
	assert.Equal(t, 3, home.Month.CategoriesCount, "the active categories, used or not")
}

func TestGetHome_ShowsOnlyTheCallersLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	othersCategory := testutil.NewCategory(t, f.pool, other.ID)
	f.spend(t, other, othersCategory.ID, 4200, 2025, time.July, 10)
	f.spend(t, other, othersCategory.ID, 1300, 2025, time.June, 10)
	f.earn(t, other, 90000, 2025, time.July, 1)
	mine := f.earn(t, owner, 250, 2025, time.July, 10)

	home := f.home(t, owner)

	assert.EqualValues(t, 250, home.BalanceMinor)
	assert.Zero(t, home.Month.SpentMinor)
	assert.Zero(t, home.Month.PreviousSpentMinor)
	assert.Zero(t, home.Month.CategoriesCount)
	assert.Empty(t, home.Month.Segments)
	require.Len(t, home.Recent.Items, 1)
	assert.Equal(t, mine.ID, home.Recent.Items[0].ID)
	require.Len(t, home.Recent.Days, 1)
	assert.EqualValues(t, 250, home.Recent.Days[0].NetMinor)
}

func TestGetHome_RequiresAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/home", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}
