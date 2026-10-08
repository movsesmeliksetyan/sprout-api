package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/api"
	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// Amounts that make the test ledger misbehave.
const (
	amountInvalid = 422
	amountBroken  = 500
	amountPanics  = 666
)

// ledgerAPI stands in for the transactions feature: creating a transaction
// inserts a row into a scratch table.
type ledgerAPI struct {
	rest
	pool  *pgxpool.Pool
	calls atomic.Int32
	// entered, when set, is called once the handler is running; it may block.
	entered func()
}

type rest struct{ httpx.NotImplemented }

func (l *ledgerAPI) CreateTransaction(ctx context.Context, request httpx.CreateTransactionRequestObject) (httpx.CreateTransactionResponseObject, error) {
	l.calls.Add(1)
	if l.entered != nil {
		l.entered()
	}
	user, _ := session.User(ctx)

	switch request.Body.AmountMinor {
	case amountInvalid:
		return nil, &httpx.ValidationError{Fields: map[string]string{"amount_minor": "is cursed"}}
	case amountBroken:
		return nil, errors.New("the ledger is on fire")
	case amountPanics:
		panic("the ledger exploded")
	}

	id := uuid.Must(uuid.NewV7())
	if _, err := l.pool.Exec(ctx, "INSERT INTO ledger (id, user_id, amount_minor) VALUES ($1, $2, $3)",
		id, user.ID, request.Body.AmountMinor); err != nil {
		return nil, err
	}
	return httpx.CreateTransaction201JSONResponse{ID: id, AmountMinor: request.Body.AmountMinor, Kind: request.Body.Kind}, nil
}

type idempotencyFixture struct {
	pool   *pgxpool.Pool
	ledger *ledgerAPI
	server http.Handler
}

func newIdempotencyFixture(t *testing.T, cfg httpx.IdempotencyConfig) *idempotencyFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	_, err := pool.Exec(context.Background(),
		"CREATE TABLE ledger (id uuid PRIMARY KEY, user_id uuid NOT NULL, amount_minor bigint NOT NULL)")
	require.NoError(t, err)

	routes, err := api.IdempotentRoutes()
	require.NoError(t, err)
	cfg.Routes = routes

	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	ledger := &ledgerAPI{pool: pool}
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
			httpx.Idempotency(pool, responder, logger, cfg),
		),
		httpx.WithAPI(ledger),
	).Handler()
	return &idempotencyFixture{pool: pool, ledger: ledger, server: server}
}

// create sends a transaction of the given amount. An empty key sends no
// Idempotency-Key header.
func (f *idempotencyFixture) create(t *testing.T, user testutil.TestUser, key string, amount int) *httptest.ResponseRecorder {
	t.Helper()
	req := testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1/transactions",
		map[string]any{"kind": "income", "amount_minor": amount})
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return testutil.Do(f.server, req)
}

func (f *idempotencyFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestIdempotency_SequentialDuplicate(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user := testutil.NewTestUser(t, f.pool)
	key := uuid.NewString()

	first := f.create(t, user, key, 1250)
	second := f.create(t, user, key, 1250)

	require.Equal(t, http.StatusCreated, first.Code, "body: %s", first.Body.String())
	assert.Empty(t, first.Header().Get("Idempotency-Replayed"))

	assert.Equal(t, http.StatusCreated, second.Code)
	assert.Equal(t, first.Body.String(), second.Body.String(), "the replay is the first response, byte for byte")
	assert.Equal(t, first.Header().Get("Content-Type"), second.Header().Get("Content-Type"))
	assert.Equal(t, "true", second.Header().Get("Idempotency-Replayed"))

	assert.Equal(t, 1, f.count(t, "ledger"), "exactly one row")
	assert.EqualValues(t, 1, f.ledger.calls.Load(), "the handler ran once")
}

func TestIdempotency_ParallelDuplicates(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	// Hold the first request in the handler so the others meet it in flight.
	f.ledger.entered = func() { time.Sleep(200 * time.Millisecond) }
	user := testutil.NewTestUser(t, f.pool)
	key := uuid.NewString()

	const requests = 10
	responses := make([]*httptest.ResponseRecorder, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[i] = f.create(t, user, key, 1250)
		}()
	}
	close(start)
	wg.Wait()

	replayed := 0
	for _, rec := range responses {
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, responses[0].Body.String(), rec.Body.String(), "every caller gets the same transaction")
		if rec.Header().Get("Idempotency-Replayed") == "true" {
			replayed++
		}
	}
	assert.Equal(t, requests-1, replayed)
	assert.Equal(t, 1, f.count(t, "ledger"), "exactly one row")
	assert.EqualValues(t, 1, f.ledger.calls.Load())
}

func TestIdempotency_KeyReuse(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	key := uuid.NewString()
	require.Equal(t, http.StatusCreated, f.create(t, user, key, 1250).Code)

	t.Run("a different body is a conflict", func(t *testing.T) {
		rec := f.create(t, user, key, 9999)

		require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		assert.Contains(t, rec.Body.String(), "different request")
		assert.Equal(t, 1, f.count(t, "ledger"))
	})

	t.Run("the original still replays afterwards", func(t *testing.T) {
		rec := f.create(t, user, key, 1250)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, "true", rec.Header().Get("Idempotency-Replayed"))
	})

	t.Run("another user's identical key is their own", func(t *testing.T) {
		rec := f.create(t, other, key, 1250)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Empty(t, rec.Header().Get("Idempotency-Replayed"))
		assert.Equal(t, 2, f.count(t, "ledger"))
	})

	t.Run("a new key runs again", func(t *testing.T) {
		rec := f.create(t, user, uuid.NewString(), 1250)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, 3, f.count(t, "ledger"))
	})
}

func TestIdempotency_OnlyWhereAsked(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user := testutil.NewTestUser(t, f.pool)

	t.Run("without the header every request runs", func(t *testing.T) {
		require.Equal(t, http.StatusCreated, f.create(t, user, "", 100).Code)
		require.Equal(t, http.StatusCreated, f.create(t, user, "", 100).Code)

		assert.Equal(t, 2, f.count(t, "ledger"))
		assert.Equal(t, 0, f.count(t, "idempotency_keys"))
	})

	t.Run("a key that is not a UUID", func(t *testing.T) {
		rec := f.create(t, user, "tuesday", 100)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, 2, f.count(t, "ledger"))
	})

	t.Run("the header means nothing on other routes", func(t *testing.T) {
		for _, req := range []*http.Request{
			testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1/uploads", map[string]any{}),
			testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/transactions", nil),
		} {
			req.Header.Set("Idempotency-Key", uuid.NewString())

			rec := testutil.Do(f.server, req)

			assert.Equal(t, http.StatusNotImplemented, rec.Code, "%s %s", req.Method, req.URL.Path)
		}
		assert.Equal(t, 0, f.count(t, "idempotency_keys"))
	})

	t.Run("no token", func(t *testing.T) {
		req := testutil.JSONRequest(t, http.MethodPost, "/v1/transactions", map[string]any{"kind": "income", "amount_minor": 5})
		req.Header.Set("Idempotency-Key", uuid.NewString())

		assert.Equal(t, http.StatusUnauthorized, testutil.Do(f.server, req).Code)
	})
}

func TestIdempotency_FailuresAreNotKept(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user := testutil.NewTestUser(t, f.pool)

	tests := []struct {
		name       string
		amount     int
		wantStatus int
	}{
		{"a validation error", amountInvalid, http.StatusUnprocessableEntity},
		{"a server error", amountBroken, http.StatusInternalServerError},
		{"a panic", amountPanics, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := uuid.NewString()
			before := f.ledger.calls.Load()

			first := f.create(t, user, key, tt.amount)
			second := f.create(t, user, key, tt.amount)

			assert.Equal(t, tt.wantStatus, first.Code, "body: %s", first.Body.String())
			assert.Equal(t, tt.wantStatus, second.Code)
			assert.Empty(t, second.Header().Get("Idempotency-Replayed"))
			assert.EqualValues(t, before+2, f.ledger.calls.Load(), "the retry ran the handler again")
			assert.Equal(t, 0, f.count(t, "idempotency_keys"), "the key was given up")
		})
	}
}

func TestIdempotency_ExpiryAndPurge(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user := testutil.NewTestUser(t, f.pool)
	ctx := context.Background()
	expired, live := uuid.NewString(), uuid.NewString()
	require.Equal(t, http.StatusCreated, f.create(t, user, expired, 100).Code)
	require.Equal(t, http.StatusCreated, f.create(t, user, live, 200).Code)

	_, err := f.pool.Exec(ctx, "UPDATE idempotency_keys SET expires_at = now() - interval '1 minute' WHERE key = $1", expired)
	require.NoError(t, err)

	t.Run("an expired key no longer replays", func(t *testing.T) {
		rec := f.create(t, user, expired, 100)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Empty(t, rec.Header().Get("Idempotency-Replayed"))
		assert.Equal(t, 3, f.count(t, "ledger"))
	})

	t.Run("purge removes expired keys only", func(t *testing.T) {
		_, err := f.pool.Exec(ctx, "UPDATE idempotency_keys SET expires_at = now() - interval '1 minute' WHERE key = $1", expired)
		require.NoError(t, err)

		removed, err := httpx.PurgeIdempotencyKeys(ctx, f.pool)

		require.NoError(t, err)
		assert.EqualValues(t, 1, removed)
		assert.Equal(t, 1, f.count(t, "idempotency_keys"))
		assert.Equal(t, "true", f.create(t, user, live, 200).Header().Get("Idempotency-Replayed"))
	})
}

func TestIdempotency_RequestStillInFlight(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{Wait: 150 * time.Millisecond, Poll: 20 * time.Millisecond})
	user := testutil.NewTestUser(t, f.pool)
	key := uuid.NewString()

	inHandler, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.ledger.entered = func() {
		once.Do(func() {
			close(inHandler)
			<-release
		})
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.create(t, user, key, 1250) }()
	<-inHandler

	t.Run("a duplicate gives up after the wait", func(t *testing.T) {
		started := time.Now()

		rec := f.create(t, user, key, 1250)

		assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		assert.Contains(t, rec.Body.String(), "still being processed")
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
		assert.GreaterOrEqual(t, time.Since(started), 150*time.Millisecond)
	})

	close(release)
	first := <-done
	require.Equal(t, http.StatusCreated, first.Code)

	t.Run("and replays once the first has finished", func(t *testing.T) {
		rec := f.create(t, user, key, 1250)

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, first.Body.String(), rec.Body.String())
		assert.Equal(t, 1, f.count(t, "ledger"))
	})
}

func TestIdempotency_AbandonedRequestIsTakenOver(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{Wait: 100 * time.Millisecond, Poll: 20 * time.Millisecond})
	user := testutil.NewTestUser(t, f.pool)
	key := uuid.New()
	ctx := context.Background()

	// A request claimed the key and its process died before answering.
	insert := func(lockedUntil string) {
		_, err := f.pool.Exec(ctx, `
			INSERT INTO idempotency_keys (user_id, key, request_hash, locked_until, expires_at)
			VALUES ($1, $2, '\x00', now() + $3::interval, now() + interval '1 day')
			ON CONFLICT (user_id, key) DO UPDATE SET locked_until = EXCLUDED.locked_until`,
			user.ID, key, lockedUntil)
		require.NoError(t, err)
	}

	t.Run("while its lock holds the key is busy", func(t *testing.T) {
		insert("1 minute")

		rec := f.create(t, user, key.String(), 1250)

		assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, 0, f.count(t, "ledger"))
	})

	t.Run("once the lock has run out the retry takes the key", func(t *testing.T) {
		insert("-1 second")

		rec := f.create(t, user, key.String(), 1250)

		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, 1, f.count(t, "ledger"))
		assert.Equal(t, "true", f.create(t, user, key.String(), 1250).Header().Get("Idempotency-Replayed"))
	})
}

func TestPurgeIdempotencyKeysEvery(t *testing.T) {
	t.Parallel()
	f := newIdempotencyFixture(t, httpx.IdempotencyConfig{})
	user := testutil.NewTestUser(t, f.pool)
	require.Equal(t, http.StatusCreated, f.create(t, user, uuid.NewString(), 100).Code)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		httpx.PurgeIdempotencyKeysEvery(ctx, f.pool, testutil.Logger(t), 20*time.Millisecond)
	}()

	// Nothing has expired yet; then the key does, and a later round removes it.
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, 1, f.count(t, "idempotency_keys"))
	_, err := f.pool.Exec(context.Background(), "UPDATE idempotency_keys SET expires_at = now() - interval '1 minute'")
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return f.count(t, "idempotency_keys") == 0 }, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge loop did not stop when its context was cancelled")
	}
}
