package transactions_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/api"
	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// Every feature calls its handler type Handler; embedding needs distinct
// field names.
type (
	usersAPI        = users.Handler
	categoriesAPI   = categories.Handler
	transactionsAPI = transactions.Handler
)

// handlers is the server's API with the transactions handler and the two that
// ask it whether transactions exist, wired as the binary wires them.
type handlers struct {
	*usersAPI
	*categoriesAPI
	*transactionsAPI
	rest
}

type rest struct{ httpx.NotImplemented }

// fixture is a server with a real Postgres behind it.
type fixture struct {
	pool   *pgxpool.Pool
	server http.Handler
}

func newFixture(t *testing.T, opts ...transactions.Option) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	routes, err := api.IdempotentRoutes()
	require.NoError(t, err)

	userService := users.NewService(pool, users.WithTransactionCheck(transactions.HasAny))
	categoryService := categories.NewService(pool, categories.WithUsageCheck(transactions.CategoryInUse))
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(userService, responder),
			httpx.Idempotency(pool, responder, logger, httpx.IdempotencyConfig{Routes: routes}),
		),
		httpx.WithAPI(handlers{
			usersAPI:        users.NewHandler(userService),
			categoriesAPI:   categories.NewHandler(categoryService),
			transactionsAPI: transactions.NewHandler(transactions.NewService(pool, opts...)),
		}),
	).Handler()
	return fixture{pool: pool, server: server}
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body))
}

// create posts a transaction and returns it as the API answered.
func (f fixture) create(t *testing.T, user testutil.TestUser, body map[string]any) httpx.Transaction {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, "/transactions", body)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Transaction](t, rec)
}

// patch updates a transaction and returns it as the API answered.
func (f fixture) patch(t *testing.T, user testutil.TestUser, id uuid.UUID, body map[string]any) httpx.Transaction {
	t.Helper()
	rec := f.do(t, user, http.MethodPatch, "/transactions/"+id.String(), body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Transaction](t, rec)
}

func (f fixture) stored(t *testing.T, user testutil.TestUser, id uuid.UUID) db.Transaction {
	t.Helper()
	transaction, err := db.New(f.pool).GetTransaction(context.Background(), db.GetTransactionParams{ID: id, UserID: user.ID})
	require.NoError(t, err)
	return transaction
}

func (f fixture) count(t *testing.T, user testutil.TestUser) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM transactions WHERE user_id = $1", user.ID).Scan(&n))
	return n
}

// setTimezone moves the user to another timezone, as PATCH /me does.
func (f fixture) setTimezone(t *testing.T, user testutil.TestUser, timezone string) {
	t.Helper()
	rec := f.do(t, user, http.MethodPatch, "/me", map[string]any{"timezone": timezone})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

type errorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

// requireError checks the status and code of an error response and returns
// its body.
func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	body := testutil.DecodeJSON[errorBody](t, rec)
	require.Equal(t, code, body.Error.Code)
	return body
}

func localDate(transaction httpx.Transaction) string {
	return transaction.LocalDate.Format(time.DateOnly)
}

func TestCreateTransaction_Expense(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)

	rec := f.do(t, user, http.MethodPost, "/transactions", map[string]any{
		"kind":         "expense",
		"amount_minor": 2440,
		"category_id":  category.ID,
		"merchant":     "  Tesco ",
		"note":         " Groceries ",
		"occurred_at":  "2025-07-21T11:02:00Z",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	created := testutil.DecodeJSON[httpx.Transaction](t, rec)

	assert.Equal(t, httpx.TransactionKind("expense"), created.Kind)
	assert.EqualValues(t, 2440, created.AmountMinor)
	require.NotNil(t, created.CategoryID)
	assert.Equal(t, category.ID, *created.CategoryID)
	require.NotNil(t, created.Merchant)
	assert.Equal(t, "Tesco", *created.Merchant)
	require.NotNil(t, created.Note)
	assert.Equal(t, "Groceries", *created.Note)
	assert.Equal(t, "2025-07-21T11:02:00Z", created.OccurredAt.Format(time.RFC3339))
	assert.Equal(t, "2025-07-21", localDate(created))
	assert.Equal(t, httpx.TransactionSource("manual"), created.Source)
	assert.Equal(t, 7, int(created.ID.Version()), "ids are UUID v7")

	stored := f.stored(t, user, created.ID)
	assert.Equal(t, "tesco", stored.MerchantKey)
	assert.Equal(t,
		transactions.DedupHash(stored.LocalDate, 2440, transactions.KindExpense, "tesco"), stored.DedupHash)
}

func TestCreateTransaction_IncomeSendsNullsAndDefaultsToNow(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)
	f := newFixture(t, transactions.WithClock(func() time.Time { return now }))
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodPost, "/transactions", map[string]any{"kind": "income", "amount_minor": 150000})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	for _, field := range []string{"category_id", "merchant", "note", "receipt_id", "import_id"} {
		value, present := body[field]
		assert.True(t, present, "%s is present", field)
		assert.Nil(t, value, "%s is null", field)
	}
	assert.Equal(t, "2025-07-21T17:24:00Z", body["occurred_at"])
	assert.Equal(t, "2025-07-21", body["local_date"])
	assert.Equal(t, "income", body["kind"])
}

func TestCreateTransaction_BlankTextIsStoredAsNull(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	created := f.create(t, user, map[string]any{"kind": "income", "amount_minor": 1, "merchant": "   ", "note": ""})

	assert.Nil(t, created.Merchant)
	assert.Nil(t, created.Note)
	assert.Empty(t, f.stored(t, user, created.ID).MerchantKey)
}

func TestCreateTransaction_LocalDateFollowsTheUsersTimezone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	f.setTimezone(t, user, "Asia/Yerevan") // UTC+4

	late := f.create(t, user, map[string]any{"kind": "income", "amount_minor": 1, "occurred_at": "2025-07-21T20:30:00Z"})
	early := f.create(t, user, map[string]any{"kind": "income", "amount_minor": 1, "occurred_at": "2025-07-21T19:59:59Z"})

	assert.Equal(t, "2025-07-22", localDate(late), "past midnight in Yerevan")
	assert.Equal(t, "2025-07-21", localDate(early))
}

func TestCreateTransaction_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	active := testutil.NewCategory(t, f.pool, user.ID).ID
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryArchived()).ID
	foreign := testutil.NewCategory(t, f.pool, other.ID).ID

	tests := []struct {
		name   string
		body   map[string]any
		fields []string
	}{
		{"zero amount", map[string]any{"kind": "income", "amount_minor": 0}, []string{"amount_minor"}},
		{"negative amount", map[string]any{"kind": "income", "amount_minor": -5}, []string{"amount_minor"}},
		{"missing amount", map[string]any{"kind": "income"}, []string{"amount_minor"}},
		{"amount out of range", map[string]any{"kind": "income", "amount_minor": 1_000_000_000_001}, []string{"amount_minor"}},
		{"unknown kind", map[string]any{"kind": "transfer", "amount_minor": 100}, []string{"kind"}},
		{"missing kind", map[string]any{"amount_minor": 100}, []string{"kind"}},
		{"expense without a category", map[string]any{"kind": "expense", "amount_minor": 100}, []string{"category_id"}},
		{"expense with an unknown category", map[string]any{"kind": "expense", "amount_minor": 100, "category_id": uuid.New()}, []string{"category_id"}},
		{"expense with an archived category", map[string]any{"kind": "expense", "amount_minor": 100, "category_id": archived}, []string{"category_id"}},
		{"expense with someone else's category", map[string]any{"kind": "expense", "amount_minor": 100, "category_id": foreign}, []string{"category_id"}},
		{"income with a category", map[string]any{"kind": "income", "amount_minor": 100, "category_id": active}, []string{"category_id"}},
		{"long merchant", map[string]any{"kind": "income", "amount_minor": 100, "merchant": strings.Repeat("m", 81)}, []string{"merchant"}},
		{"long note", map[string]any{"kind": "income", "amount_minor": 100, "note": strings.Repeat("n", 141)}, []string{"note"}},
		{"everything at once", map[string]any{
			"kind": "expense", "amount_minor": 0, "category_id": archived,
			"merchant": strings.Repeat("m", 81), "note": strings.Repeat("n", 141),
		}, []string{"amount_minor", "category_id", "merchant", "note"}},
	}
	for _, tt := range tests {
		rec := f.do(t, user, http.MethodPost, "/transactions", tt.body)

		body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		fields := make([]string, 0, len(body.Error.Fields))
		for field := range body.Error.Fields {
			fields = append(fields, field)
		}
		assert.ElementsMatch(t, tt.fields, fields, tt.name)
	}
	assert.Zero(t, f.count(t, user), "nothing was written")
}

func TestCreateTransaction_LimitsAreInclusive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	created := f.create(t, user, map[string]any{
		"kind": "income", "amount_minor": 1_000_000_000_000,
		"merchant": strings.Repeat("é", 80), "note": strings.Repeat("n", 140),
	})

	assert.EqualValues(t, 1_000_000_000_000, created.AmountMinor)
}

func TestCreateTransaction_MalformedBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodPost, "/transactions", map[string]any{"kind": "income", "amount_minor": "lots"})

	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestCreateTransaction_Idempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	key := uuid.NewString()
	post := func(amount int) *httptest.ResponseRecorder {
		req := testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1/transactions",
			map[string]any{"kind": "income", "amount_minor": amount})
		req.Header.Set("Idempotency-Key", key)
		return testutil.Do(f.server, req)
	}

	first := post(500)
	second := post(500)
	different := post(501)

	require.Equal(t, http.StatusCreated, first.Code, "body: %s", first.Body.String())
	require.Equal(t, http.StatusCreated, second.Code, "body: %s", second.Body.String())
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.Equal(t, "true", second.Header().Get("Idempotency-Replayed"))
	requireError(t, different, http.StatusConflict, "conflict")
	assert.Equal(t, 1, f.count(t, user))
}

func TestGetTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	seeded := testutil.NewTransaction(t, f.pool, user.User,
		testutil.TransactionExpense(category.ID), testutil.TransactionAmount(4800), testutil.TransactionMerchant("Shell"))

	rec := f.do(t, user, http.MethodGet, "/transactions/"+seeded.ID.String(), nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	got := testutil.DecodeJSON[httpx.Transaction](t, rec)
	assert.Equal(t, seeded.ID, got.ID)
	assert.EqualValues(t, 4800, got.AmountMinor)
	require.NotNil(t, got.Merchant)
	assert.Equal(t, "Shell", *got.Merchant)
	assert.Equal(t, httpx.Instant(seeded.CreatedAt), got.CreatedAt)
}

func TestGetTransaction_Unknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	requireError(t, f.do(t, user, http.MethodGet, "/transactions/"+uuid.NewString(), nil), http.StatusNotFound, "not_found")
	requireError(t, f.do(t, user, http.MethodGet, "/transactions/not-a-uuid", nil), http.StatusBadRequest, "bad_request")
}

func TestUpdateTransaction_EachField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID)
	transport := testutil.NewCategory(t, f.pool, user.ID)
	at := time.Date(2025, 7, 21, 11, 2, 0, 0, time.UTC)
	seed := func() db.Transaction {
		return testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(food.ID),
			testutil.TransactionAmount(2440), testutil.TransactionMerchant("Tesco"),
			testutil.TransactionNote("Groceries"), testutil.TransactionAt(at))
	}

	tests := []struct {
		name string
		body map[string]any
		want func(tx *db.Transaction)
	}{
		{"amount", map[string]any{"amount_minor": 2500}, func(tx *db.Transaction) { tx.AmountMinor = 2500 }},
		{"category", map[string]any{"category_id": transport.ID}, func(tx *db.Transaction) { tx.CategoryID = &transport.ID }},
		{"merchant", map[string]any{"merchant": " Asda "}, func(tx *db.Transaction) {
			merchant := "Asda"
			tx.Merchant, tx.MerchantKey = &merchant, "asda"
		}},
		{"merchant cleared", map[string]any{"merchant": nil}, func(tx *db.Transaction) { tx.Merchant, tx.MerchantKey = nil, "" }},
		{"note", map[string]any{"note": "Weekly shop"}, func(tx *db.Transaction) {
			note := "Weekly shop"
			tx.Note = &note
		}},
		{"note cleared", map[string]any{"note": nil}, func(tx *db.Transaction) { tx.Note = nil }},
		{"note blanked", map[string]any{"note": "  "}, func(tx *db.Transaction) { tx.Note = nil }},
		{"time", map[string]any{"occurred_at": "2025-07-19T08:00:00Z"}, func(tx *db.Transaction) {
			tx.OccurredAt = time.Date(2025, 7, 19, 8, 0, 0, 0, time.UTC)
			tx.LocalDate = time.Date(2025, 7, 19, 0, 0, 0, 0, time.UTC)
		}},
		{"kind to income drops the category", map[string]any{"kind": "income"}, func(tx *db.Transaction) {
			tx.Kind, tx.CategoryID = "income", nil
		}},
		{"kind to income with a null category", map[string]any{"kind": "income", "category_id": nil}, func(tx *db.Transaction) {
			tx.Kind, tx.CategoryID = "income", nil
		}},
		{"nothing", map[string]any{}, func(*db.Transaction) {}},
	}
	for _, tt := range tests {
		want := seed()
		tt.want(&want)
		want.DedupHash = transactions.DedupHash(want.LocalDate, want.AmountMinor, transactions.Kind(want.Kind), want.MerchantKey)

		f.patch(t, user, want.ID, tt.body)

		got := f.stored(t, user, want.ID)
		assert.True(t, want.OccurredAt.Equal(got.OccurredAt), "%s: occurred_at", tt.name)
		assert.True(t, want.LocalDate.Equal(got.LocalDate), "%s: local_date", tt.name)
		got.OccurredAt, got.LocalDate, got.UpdatedAt = want.OccurredAt, want.LocalDate, want.UpdatedAt
		assert.Equal(t, want, got, tt.name)
	}
}

func TestUpdateTransaction_IncomeToExpenseNeedsACategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	income := testutil.NewTransaction(t, f.pool, user.User)

	rec := f.do(t, user, http.MethodPatch, "/transactions/"+income.ID.String(), map[string]any{"kind": "expense"})
	body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	assert.Contains(t, body.Error.Fields, "category_id")

	updated := f.patch(t, user, income.ID, map[string]any{"kind": "expense", "category_id": category.ID})
	assert.Equal(t, httpx.TransactionKind("expense"), updated.Kind)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, category.ID, *updated.CategoryID)
}

func TestUpdateTransaction_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryArchived()).ID
	foreign := testutil.NewCategory(t, f.pool, other.ID).ID
	expense := testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID))
	income := testutil.NewTransaction(t, f.pool, user.User)

	tests := []struct {
		name   string
		id     uuid.UUID
		body   map[string]any
		fields []string
	}{
		{"zero amount", expense.ID, map[string]any{"amount_minor": 0}, []string{"amount_minor"}},
		{"amount out of range", expense.ID, map[string]any{"amount_minor": 1_000_000_000_001}, []string{"amount_minor"}},
		{"unknown kind", expense.ID, map[string]any{"kind": "transfer"}, []string{"kind"}},
		{"expense loses its category", expense.ID, map[string]any{"category_id": nil}, []string{"category_id"}},
		{"unknown category", expense.ID, map[string]any{"category_id": uuid.New()}, []string{"category_id"}},
		{"archived category", expense.ID, map[string]any{"category_id": archived}, []string{"category_id"}},
		{"someone else's category", expense.ID, map[string]any{"category_id": foreign}, []string{"category_id"}},
		{"income given a category", income.ID, map[string]any{"category_id": category.ID}, []string{"category_id"}},
		{"to income but keeping a category", expense.ID, map[string]any{"kind": "income", "category_id": category.ID}, []string{"category_id"}},
		{"long merchant", expense.ID, map[string]any{"merchant": strings.Repeat("m", 81)}, []string{"merchant"}},
		{"long note", expense.ID, map[string]any{"note": strings.Repeat("n", 141)}, []string{"note"}},
		{"several at once", expense.ID, map[string]any{"amount_minor": -1, "note": strings.Repeat("n", 141), "category_id": foreign},
			[]string{"amount_minor", "note", "category_id"}},
	}
	for _, tt := range tests {
		rec := f.do(t, user, http.MethodPatch, "/transactions/"+tt.id.String(), tt.body)

		body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		fields := make([]string, 0, len(body.Error.Fields))
		for field := range body.Error.Fields {
			fields = append(fields, field)
		}
		assert.ElementsMatch(t, tt.fields, fields, tt.name)
	}
	assert.Equal(t, expense, f.stored(t, user, expense.ID), "nothing was written")
	assert.Equal(t, income, f.stored(t, user, income.ID), "nothing was written")
}

func TestUpdateTransaction_KeepsACategoryArchivedLater(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	expense := testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID))
	rec := f.do(t, user, http.MethodPatch, "/categories/"+category.ID.String(), map[string]any{"archived": true})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	updated := f.patch(t, user, expense.ID, map[string]any{"amount_minor": 777, "category_id": category.ID})

	assert.EqualValues(t, 777, updated.AmountMinor)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, category.ID, *updated.CategoryID)
}

func TestUpdateTransaction_TimezoneChangeDoesNotMoveHistory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	created := f.create(t, user, map[string]any{"kind": "income", "amount_minor": 100, "occurred_at": "2025-07-21T20:30:00Z"})
	require.Equal(t, "2025-07-21", localDate(created))

	f.setTimezone(t, user, "Asia/Yerevan") // UTC+4: the same instant is the 22nd

	sameTime := f.patch(t, user, created.ID, map[string]any{"amount_minor": 200, "occurred_at": "2025-07-21T20:30:00Z"})
	assert.Equal(t, "2025-07-21", localDate(sameTime), "the day it was filed under stays")

	moved := f.patch(t, user, created.ID, map[string]any{"occurred_at": "2025-07-21T20:45:00Z"})
	assert.Equal(t, "2025-07-22", localDate(moved), "a new time is read in the new timezone")
}

func TestDeleteTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	seeded := testutil.NewTransaction(t, f.pool, user.User)
	path := "/transactions/" + seeded.ID.String()

	rec := f.do(t, user, http.MethodDelete, path, nil)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, rec.Body.String())
	_, err := db.New(f.pool).GetTransaction(context.Background(), db.GetTransactionParams{ID: seeded.ID, UserID: user.ID})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	requireError(t, f.do(t, user, http.MethodDelete, path, nil), http.StatusNotFound, "not_found")
	requireError(t, f.do(t, user, http.MethodGet, path, nil), http.StatusNotFound, "not_found")
}

func TestTransactions_AnotherUsersTransactionIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	intruder := testutil.NewTestUser(t, f.pool)
	seeded := testutil.NewTransaction(t, f.pool, owner.User, testutil.TransactionAmount(4200))
	path := "/transactions/" + seeded.ID.String()

	requireError(t, f.do(t, intruder, http.MethodGet, path, nil), http.StatusNotFound, "not_found")
	requireError(t, f.do(t, intruder, http.MethodPatch, path, map[string]any{"amount_minor": 1}), http.StatusNotFound, "not_found")
	requireError(t, f.do(t, intruder, http.MethodDelete, path, nil), http.StatusNotFound, "not_found")

	assert.Equal(t, seeded, f.stored(t, owner, seeded.ID), "the owner's transaction is untouched")
}

func TestTransactions_RequireAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodPost, "/v1/transactions",
		map[string]any{"kind": "income", "amount_minor": 100}))

	requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
}

func TestMerchantRuleHook(t *testing.T) {
	t.Parallel()
	type call struct {
		userID      uuid.UUID
		merchantKey string
		categoryID  uuid.UUID
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	f := newFixture(t, transactions.WithMerchantRuleHook(
		func(_ context.Context, _ db.Querier, userID uuid.UUID, merchantKey string, categoryID uuid.UUID) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, call{userID, merchantKey, categoryID})
			return nil
		}))
	user := testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID)
	transport := testutil.NewCategory(t, f.pool, user.ID)

	f.create(t, user, map[string]any{"kind": "income", "amount_minor": 100, "merchant": "Employer"})
	f.create(t, user, map[string]any{"kind": "expense", "amount_minor": 100, "category_id": food.ID})
	require.Empty(t, calls, "income and expenses without a merchant teach nothing")

	created := f.create(t, user, map[string]any{"kind": "expense", "amount_minor": 100, "category_id": food.ID, "merchant": "Tesco"})
	f.patch(t, user, created.ID, map[string]any{"category_id": transport.ID})

	assert.Equal(t, []call{{user.ID, "tesco", food.ID}, {user.ID, "tesco", transport.ID}}, calls)
}

func TestMerchantRuleHook_FailureUndoesTheSave(t *testing.T) {
	t.Parallel()
	f := newFixture(t, transactions.WithMerchantRuleHook(
		func(context.Context, db.Querier, uuid.UUID, string, uuid.UUID) error { return assert.AnError }))
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)

	rec := f.do(t, user, http.MethodPost, "/transactions",
		map[string]any{"kind": "expense", "amount_minor": 100, "category_id": category.ID, "merchant": "Tesco"})

	requireError(t, rec, http.StatusInternalServerError, "internal")
	assert.Zero(t, f.count(t, user))
}

func TestTransactionsLockTheCurrency(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodPatch, "/me", map[string]any{"currency": "EUR"})
	require.Equal(t, http.StatusOK, rec.Code, "free to change with an empty ledger; body: %s", rec.Body.String())

	created := f.create(t, user, map[string]any{"kind": "income", "amount_minor": 100})
	requireError(t, f.do(t, user, http.MethodPatch, "/me", map[string]any{"currency": "GBP"}), http.StatusConflict, "conflict")

	rec = f.do(t, user, http.MethodDelete, "/transactions/"+created.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = f.do(t, user, http.MethodPatch, "/me", map[string]any{"currency": "GBP"})
	require.Equal(t, http.StatusOK, rec.Code, "free again once the ledger is empty; body: %s", rec.Body.String())
}

func TestTransactionsKeepTheirCategoryFromDeletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID)
	unused := testutil.NewCategory(t, f.pool, user.ID)
	expense := testutil.NewTransaction(t, f.pool, user.User, testutil.TransactionExpense(category.ID))

	requireError(t, f.do(t, user, http.MethodDelete, "/categories/"+category.ID.String(), nil), http.StatusConflict, "conflict")
	rec := f.do(t, user, http.MethodDelete, "/categories/"+unused.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	rec = f.do(t, user, http.MethodDelete, "/transactions/"+expense.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = f.do(t, user, http.MethodDelete, "/categories/"+category.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
}
