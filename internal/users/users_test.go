package users_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

var daisy = auth.Claims{Subject: "apple|001234.abcd", Email: "daisy.walker@example.com", Name: "Daisy Walker"}

func userCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&n))
	return n
}

func TestProvision_CreatesTheUserWithDefaults(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)

	user, err := users.NewService(pool).Provision(context.Background(), daisy)

	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), user.ID.Version())
	assert.Equal(t, daisy.Subject, user.Auth0Sub)
	assert.Equal(t, "daisy.walker@example.com", user.Email)
	assert.Equal(t, "Daisy Walker", user.Name)
	assert.Nil(t, user.AvatarKey)
	assert.Equal(t, "USD", user.Currency)
	assert.Equal(t, "UTC", user.Timezone)
	assert.Zero(t, user.StartingBalanceMinor)
	assert.False(t, user.OnboardingCompleted)
	assert.True(t, user.NotificationsEnabled)
	assert.True(t, user.BudgetAlerts)
	assert.True(t, user.WeeklyRecap)
	assert.WithinDuration(t, time.Now(), user.CreatedAt, time.Minute)
}

func TestProvision_NameAndEmailFromClaims(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	service := users.NewService(pool)

	tests := []struct {
		name      string
		claims    auth.Claims
		wantEmail string
		wantName  string
	}{
		{"neither shared", auth.Claims{Subject: "apple|1"}, "", ""},
		{"surrounding space", auth.Claims{Subject: "apple|2", Email: " sam@example.com ", Name: "  Sam Example "}, "sam@example.com", "Sam Example"},
		{"Auth0 put the email in the name", auth.Claims{Subject: "email|3", Email: "sam@example.com", Name: "sam@example.com"}, "sam@example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := service.Provision(context.Background(), tt.claims)

			require.NoError(t, err)
			assert.Equal(t, tt.wantEmail, user.Email)
			assert.Equal(t, tt.wantName, user.Name)
		})
	}

	_, err := service.Provision(context.Background(), auth.Claims{})
	assert.Error(t, err, "claims without a subject identify nobody")
}

func TestProvision_ReturnsTheExistingUserUnchanged(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	var hookCalls atomic.Int32
	service := users.NewService(pool, users.WithOnUserCreated(func(context.Context, db.Querier, db.User) error {
		hookCalls.Add(1)
		return nil
	}))
	ctx := context.Background()

	first, err := service.Provision(ctx, daisy)
	require.NoError(t, err)
	second, err := service.Provision(ctx, auth.Claims{Subject: daisy.Subject, Email: "new@example.com", Name: "Someone Else"})
	require.NoError(t, err)

	assert.Equal(t, first, second, "claims never overwrite a stored profile")
	assert.EqualValues(t, 1, hookCalls.Load())
	assert.Equal(t, 1, userCount(t, pool))
}

func TestProvision_ConcurrentFirstRequests(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	var hookCalls atomic.Int32
	service := users.NewService(pool, users.WithOnUserCreated(func(context.Context, db.Querier, db.User) error {
		hookCalls.Add(1)
		// Hold the creating transaction open so the others pile up behind it.
		time.Sleep(50 * time.Millisecond)
		return nil
	}))

	const requests = 10
	ids := make([]uuid.UUID, requests)
	errs := make([]error, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			user, err := service.Provision(context.Background(), daisy)
			ids[i], errs[i] = user.ID, err
		}()
	}
	close(start)
	wg.Wait()

	for i := range requests {
		require.NoError(t, errs[i])
		assert.Equal(t, ids[0], ids[i], "every request gets the same user")
	}
	assert.Equal(t, 1, userCount(t, pool))
	assert.EqualValues(t, 1, hookCalls.Load(), "the hook runs once per user")
}

func TestProvision_HookRunsInTheCreatingTransaction(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	ctx := context.Background()

	t.Run("a failing hook undoes the user", func(t *testing.T) {
		failing := users.NewService(pool, users.WithOnUserCreated(func(context.Context, db.Querier, db.User) error {
			return errors.New("seeding failed")
		}))

		_, err := failing.Provision(ctx, daisy)

		require.ErrorContains(t, err, "seeding failed")
		assert.Equal(t, 0, userCount(t, pool))
	})

	t.Run("the next request creates the user and runs the hook again", func(t *testing.T) {
		var seen db.User
		working := users.NewService(pool, users.WithOnUserCreated(func(ctx context.Context, q db.Querier, user db.User) error {
			// The new user is visible through q, and only through q.
			var err error
			seen, err = q.GetUserByAuth0Sub(ctx, user.Auth0Sub)
			return err
		}))

		user, err := working.Provision(ctx, daisy)

		require.NoError(t, err)
		assert.Equal(t, user, seen)
		assert.Equal(t, 1, userCount(t, pool))
	})
}

func TestFirstName(t *testing.T) {
	tests := map[string]string{
		"Daisy Walker":     "Daisy",
		"  Daisy  Walker ": "Daisy",
		"Daisy":            "Daisy",
		"Mary Jane Watson": "Mary",
		"":                 "",
		"   ":              "",
	}
	for name, want := range tests {
		assert.Equal(t, want, users.FirstName(name), "FirstName(%q)", name)
	}
}

// api is the server's API with only the users handler implemented.
type api struct {
	*users.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

func newServer(t *testing.T, pool *pgxpool.Pool, opts ...users.Option) http.Handler {
	t.Helper()
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	service := users.NewService(pool, opts...)
	return httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(service, responder),
		),
		httpx.WithAPI(api{Handler: users.NewHandler(service)}),
	).Handler()
}

func TestGetMe_FirstCallCreatesTheUser(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)

	rec := testutil.Do(server, testutil.AuthedRequest(t, daisy, http.MethodGet, "/v1/me", nil))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	id, err := uuid.Parse(body["id"].(string))
	require.NoError(t, err)
	createdAt, err := time.Parse("2006-01-02T15:04:05Z", body["created_at"].(string))
	require.NoError(t, err, "created_at is UTC to the second")
	assert.WithinDuration(t, time.Now(), createdAt, time.Minute)

	assert.Equal(t, map[string]any{
		"id":                     id.String(),
		"name":                   "Daisy Walker",
		"first_name":             "Daisy",
		"email":                  "daisy.walker@example.com",
		"avatar_url":             nil,
		"currency":               "USD",
		"timezone":               "UTC",
		"starting_balance_minor": float64(0),
		"onboarding_completed":   false,
		"preferences":            map[string]any{"notifications_enabled": true, "budget_alerts": true, "weekly_recap": true},
		"stats":                  map[string]any{"categories_count": float64(0), "goals_count": float64(0), "streak_days": float64(0)},
		"created_at":             body["created_at"],
	}, body)

	t.Run("the second call returns the same user", func(t *testing.T) {
		again := testutil.Do(server, testutil.AuthedRequest(t, daisy, http.MethodGet, "/v1/me", nil))

		require.Equal(t, http.StatusOK, again.Code)
		assert.JSONEq(t, rec.Body.String(), again.Body.String())
		assert.Equal(t, 1, userCount(t, pool))
	})
}

func TestGetMe_EachUserSeesOnlyThemselves(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)
	first, second := testutil.NewTestUser(t, pool), testutil.NewTestUser(t, pool)
	require.NotEqual(t, first.ID, second.ID)

	for _, user := range []testutil.TestUser{first, second} {
		rec := testutil.Do(server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/me", nil))

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		me := testutil.DecodeJSON[httpx.Me](t, rec)
		assert.Equal(t, user.ID, me.ID)
		assert.Equal(t, user.Email, me.Email)
	}
	assert.Equal(t, 2, userCount(t, pool))
}

func TestMiddleware(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)

	t.Run("no token creates nobody", func(t *testing.T) {
		rec := testutil.Do(server, testutil.JSONRequest(t, http.MethodGet, "/v1/me", nil))

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, 0, userCount(t, pool))
	})

	t.Run("any operation provisions the user", func(t *testing.T) {
		rec := testutil.Do(server, testutil.AuthedRequest(t, daisy, http.MethodGet, "/v1/goals", nil))

		assert.Equal(t, http.StatusNotImplemented, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, 1, userCount(t, pool))
	})

	t.Run("without verified claims the request is refused", func(t *testing.T) {
		logger := testutil.Logger(t)
		called := false
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
		handler := users.Middleware(users.NewService(pool), httpx.NewResponder(logger))(next)

		rec := testutil.Do(handler, testutil.JSONRequest(t, http.MethodGet, "/v1/me", nil))

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, called)
	})

	t.Run("the handler sees the user", func(t *testing.T) {
		logger := testutil.Logger(t)
		var got db.User
		next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = users.FromContext(r.Context())
		})
		handler := users.Middleware(users.NewService(pool), httpx.NewResponder(logger))(next)
		req := testutil.JSONRequest(t, http.MethodGet, "/v1/me", nil)

		testutil.Do(handler, req.WithContext(auth.WithClaims(req.Context(), daisy)))

		assert.Equal(t, daisy.Subject, got.Auth0Sub)
	})

	t.Run("a database failure is a 500, not a 401", func(t *testing.T) {
		closed := testutil.NewDB(t)
		closed.Close()

		rec := testutil.Do(newServer(t, closed), testutil.AuthedRequest(t, daisy, http.MethodGet, "/v1/me", nil))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "closed pool")
	})
}

func TestFromContext_OutsideARequest(t *testing.T) {
	_, ok := users.FromContext(context.Background())

	assert.False(t, ok)
}
