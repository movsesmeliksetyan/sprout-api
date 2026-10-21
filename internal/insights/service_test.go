package insights_test

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/insights"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// july21 is the moment the tests look from: a Monday afternoon in July
// 2025.
var july21 = time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)

type api struct {
	*insights.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

type fixture struct {
	pool     *pgxpool.Pool
	server   http.Handler
	service  *insights.Service
	now      *atomic.Pointer[time.Time]
	computed *atomic.Int32
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	f := fixture{pool: pool, now: &atomic.Pointer[time.Time]{}, computed: &atomic.Int32{}}
	f.now.Store(&july21)
	clock := func() time.Time { return *f.now.Load() }
	f.service = insights.NewService(pool, goals.NewService(pool, goals.WithClock(clock)),
		insights.WithClock(clock), insights.WithComputeHook(func() { f.computed.Add(1) }))
	f.server = httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: insights.NewHandler(f.service)}),
	).Handler()
	return f
}

func (f fixture) list(t *testing.T, user testutil.TestUser, query string) []httpx.Insight {
	t.Helper()
	rec := testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/insights?"+query, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return testutil.DecodeJSON[httpx.InsightList](t, rec).Items
}

func (f fixture) version(t *testing.T, user testutil.TestUser) int64 {
	t.Helper()
	v, err := db.New(f.pool).GetLedgerVersion(context.Background(), user.ID)
	require.NoError(t, err)
	return v
}

// spender is a user in dollars and UTC.
func spender(t *testing.T, f fixture) testutil.TestUser {
	t.Helper()
	user := testutil.NewTestUser(t, f.pool)
	usd, utc := "USD", "UTC"
	updated, err := db.New(f.pool).UpdateUser(context.Background(), db.UpdateUserParams{ID: user.ID, Currency: &usd, Timezone: &utc})
	require.NoError(t, err)
	user.User = updated
	return user
}

func (f fixture) spend(t *testing.T, user testutil.TestUser, category uuid.UUID, minor int64, date string, merchant ...string) {
	t.Helper()
	at, err := time.Parse(time.DateOnly, date)
	require.NoError(t, err)
	opts := []testutil.TransactionOption{testutil.TransactionExpense(category), testutil.TransactionAmount(minor), testutil.TransactionAt(at.Add(12 * time.Hour))}
	if len(merchant) > 0 {
		opts = append(opts, testutil.TransactionMerchant(merchant[0]))
	}
	testutil.NewTransaction(t, f.pool, user.User, opts...)
}

// theDesign fills July 2025 the way the app's design shows it: less on food
// than in June, self-care past its budget mostly through one spa visit,
// and Saturdays taking the most.
func (f fixture) theDesign(t *testing.T, user testutil.TestUser) (food, selfCare uuid.UUID) {
	t.Helper()
	food = testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"), testutil.CategoryBudget(150000)).ID
	selfCare = testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Self-care"), testutil.CategoryBudget(15000)).ID
	bills := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Bills")).ID

	f.spend(t, user, food, 135000, "2025-06-10")
	f.spend(t, user, selfCare, 15500, "2025-06-14")
	f.spend(t, user, bills, 30000, "2025-06-02")

	f.spend(t, user, food, 64000, "2025-07-02")
	f.spend(t, user, food, 60000, "2025-07-19")
	f.spend(t, user, selfCare, 4000, "2025-07-03")
	f.spend(t, user, selfCare, 3000, "2025-07-08")
	f.spend(t, user, selfCare, 9000, "2025-07-12", "Serenity Spa")
	f.spend(t, user, bills, 34000, "2025-07-07")
	return food, selfCare
}

func titles(cards []httpx.Insight) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Title
	}
	return out
}

func TestListInsights_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	food, selfCare := f.theDesign(t, user)

	got := f.list(t, user, "period=month")
	assert.Equal(t, []string{"Self-care is over budget", "You spent 8% less on Food", "Your biggest day was Saturday"}, titles(got))
	assert.Equal(t, "$10 above your $150 limit. Most of it was one $90 expense at Serenity Spa on the 12th.", got[0].Body)
	assert.Equal(t, httpx.InsightKind("category_over_budget"), got[0].Kind)
	assert.Equal(t, httpx.InsightTone("warning"), got[0].Tone)
	assert.Equal(t, httpx.InsightIcon("warning"), got[0].Icon)
	assert.Equal(t, &selfCare, got[0].CategoryID)
	assert.Equal(t, &food, got[1].CategoryID)
	assert.Nil(t, got[2].CategoryID)
	assert.Nil(t, got[2].GoalID)
}

func TestListInsights_ServedUntilTheLedgerChanges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	food, _ := f.theDesign(t, user)

	first := f.list(t, user, "period=month")
	require.EqualValues(t, 1, f.computed.Load())

	again := f.list(t, user, "period=month")
	assert.EqualValues(t, 1, f.computed.Load(), "an unchanged ledger is served from the table")
	assert.Equal(t, first, again, "the same cards, ids and all")

	f.spend(t, user, food, 30000, "2025-07-20")
	changed := f.list(t, user, "period=month")
	assert.EqualValues(t, 2, f.computed.Load(), "a new transaction is computed again")
	assert.NotContains(t, titles(changed), "You spent 8% less on Food", "food is now level with June")

	tomorrow := july21.Add(24 * time.Hour)
	f.now.Store(&tomorrow)
	f.list(t, user, "period=month")
	assert.EqualValues(t, 3, f.computed.Load(), "a new day is computed again")

	f.list(t, user, "period=week")
	assert.EqualValues(t, 4, f.computed.Load(), "each period has cards of its own")
	f.list(t, user, "period=month")
	assert.EqualValues(t, 4, f.computed.Load())
}

func TestLedgerVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := spender(t, f)
	other := spender(t, f)
	category := testutil.NewCategory(t, f.pool, user.ID).ID
	before, untouched := f.version(t, user), f.version(t, other)

	_, err := f.pool.Exec(ctx, `
		INSERT INTO transactions (id, user_id, kind, amount_minor, occurred_at, local_date, source, category_id, dedup_hash)
		SELECT gen_random_uuid(), $1, 'expense', 100, now(), current_date, 'manual', $2, sha256(n::text::bytea) FROM generate_series(1, 3) n`,
		user.ID, category)
	require.NoError(t, err)
	assert.Equal(t, before+1, f.version(t, user), "one statement, one bump")

	bumps := func(name string, change func()) {
		t.Helper()
		was := f.version(t, user)
		change()
		assert.Greater(t, f.version(t, user), was, name)
	}
	bumps("a budget", func() {
		_, err := f.pool.Exec(ctx, `UPDATE categories SET monthly_budget_minor = 5000 WHERE id = $1`, category)
		require.NoError(t, err)
	})
	var goal db.Goal
	bumps("a goal", func() { goal = testutil.NewGoal(t, f.pool, user.ID) })
	bumps("a contribution", func() { testutil.NewContribution(t, f.pool, user.User, goal.ID, "topup", 1000, july21) })
	bumps("a delete", func() {
		_, err := f.pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, user.ID)
		require.NoError(t, err)
	})
	bumps("the currency", func() {
		eur := "EUR"
		_, err := db.New(f.pool).UpdateUser(ctx, db.UpdateUserParams{ID: user.ID, Currency: &eur})
		require.NoError(t, err)
	})
	was := f.version(t, user)
	name := "Someone"
	_, err = db.New(f.pool).UpdateUser(ctx, db.UpdateUserParams{ID: user.ID, Name: &name})
	require.NoError(t, err)
	assert.Equal(t, was, f.version(t, user), "a name does not count")

	assert.Equal(t, untouched, f.version(t, other))
}

func TestListInsights_NoData(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	got := f.list(t, user, "period=month")
	require.Len(t, got, 1)
	assert.Equal(t, httpx.InsightKind("no_data"), got[0].Kind)

	got = f.list(t, user, "period=month&offset=2")
	require.Len(t, got, 1)
	assert.Equal(t, "There were too few expenses in May 2025 to say much about it.", got[0].Body)
}

func TestListInsights_OnlyTheUsersOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := spender(t, f)
	f.theDesign(t, owner)
	require.Len(t, f.list(t, owner, "period=month"), 3)

	other := spender(t, f)
	got := f.list(t, other, "period=month")
	require.Len(t, got, 1)
	assert.Equal(t, httpx.InsightKind("no_data"), got[0].Kind, "another user's ledger is not read")
}

func TestListInsights_Goals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	f.theDesign(t, user)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("New bike"), testutil.GoalTarget(100000))
	testutil.NewContribution(t, f.pool, user.User, goal.ID, "topup", 90000, july21.AddDate(0, 0, -10))
	testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Old"), testutil.GoalTarget(100), testutil.GoalStatus("archived"))

	got := f.list(t, user, "period=month")
	require.Len(t, got, 4)
	assert.Equal(t, "Almost there with New bike", got[1].Title)
	assert.Equal(t, &goal.ID, got[1].GoalID)
}

func TestListInsights_BadRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	for _, query := range []string{"period=decade", "period=month&offset=1201", ""} {
		rec := testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/insights?"+query, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := spender(t, f)
	f.theDesign(t, user)
	require.NoError(t, f.service.Refresh(context.Background(), user.User))
	require.EqualValues(t, 1, f.computed.Load())
	f.list(t, user, "period=month")
	assert.EqualValues(t, 1, f.computed.Load(), "the next read is served from the table")
}
