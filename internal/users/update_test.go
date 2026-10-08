package users_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func patchMe(t *testing.T, server http.Handler, user testutil.TestUser, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(server, testutil.AuthedRequest(t, user.Claims, http.MethodPatch, "/v1/me", body))
}

func storedUser(t *testing.T, pool *pgxpool.Pool, user testutil.TestUser) db.User {
	t.Helper()
	stored, err := db.New(pool).GetUserByAuth0Sub(context.Background(), user.Auth0Sub)
	require.NoError(t, err)
	return stored
}

type errorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func TestUpdateMe_EachField(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)

	tests := []struct {
		name string
		body map[string]any
		want func(u *db.User)
	}{
		{"name", map[string]any{"name": "  Daisy Walker "}, func(u *db.User) { u.Name = "Daisy Walker" }},
		{"currency", map[string]any{"currency": "GBP"}, func(u *db.User) { u.Currency = "GBP" }},
		{"currency in lower case", map[string]any{"currency": "eur"}, func(u *db.User) { u.Currency = "EUR" }},
		{"timezone", map[string]any{"timezone": "Europe/London"}, func(u *db.User) { u.Timezone = "Europe/London" }},
		{"starting balance", map[string]any{"starting_balance_minor": 40855}, func(u *db.User) { u.StartingBalanceMinor = 40855 }},
		{"negative starting balance", map[string]any{"starting_balance_minor": -12050}, func(u *db.User) { u.StartingBalanceMinor = -12050 }},
		{"onboarding completed", map[string]any{"onboarding_completed": true}, func(u *db.User) { u.OnboardingCompleted = true }},
		{"one preference", map[string]any{"preferences": map[string]any{"weekly_recap": false}}, func(u *db.User) { u.WeeklyRecap = false }},
		{"all preferences", map[string]any{"preferences": map[string]any{"notifications_enabled": false, "budget_alerts": false, "weekly_recap": false}},
			func(u *db.User) { u.NotificationsEnabled, u.BudgetAlerts, u.WeeklyRecap = false, false, false }},
		{"removing an avatar that is not there", map[string]any{"avatar_upload_id": nil}, func(*db.User) {}},
		{"nothing", map[string]any{}, func(*db.User) {}},
		{"unknown fields are ignored", map[string]any{"nickname": "D", "id": uuid.NewString()}, func(*db.User) {}},
		{"several at once", map[string]any{"name": "Sam", "timezone": "Asia/Yerevan", "onboarding_completed": true},
			func(u *db.User) { u.Name, u.Timezone, u.OnboardingCompleted = "Sam", "Asia/Yerevan", true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := testutil.NewTestUser(t, pool)
			want := user.User
			tt.want(&want)

			rec := patchMe(t, server, user, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			got := storedUser(t, pool, user)
			assert.False(t, got.UpdatedAt.Before(user.UpdatedAt))
			got.UpdatedAt = want.UpdatedAt
			assert.Equal(t, want, got, "only the patched fields change")

			me := testutil.DecodeJSON[httpx.Me](t, rec)
			assert.Equal(t, want.ID, me.ID)
			assert.Equal(t, want.Name, me.Name)
			assert.Equal(t, users.FirstName(want.Name), me.FirstName)
			assert.Equal(t, want.Currency, me.Currency)
			assert.Equal(t, want.Timezone, me.Timezone)
			assert.Equal(t, want.StartingBalanceMinor, me.StartingBalanceMinor)
			assert.Equal(t, want.OnboardingCompleted, me.OnboardingCompleted)
			assert.Equal(t, httpx.Preferences{
				NotificationsEnabled: want.NotificationsEnabled,
				BudgetAlerts:         want.BudgetAlerts,
				WeeklyRecap:          want.WeeklyRecap,
			}, me.Preferences)
		})
	}
}

func TestUpdateMe_InvalidValues(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)
	user := testutil.NewTestUser(t, pool)

	tests := []struct {
		name       string
		body       map[string]any
		wantFields []string
	}{
		{"empty name", map[string]any{"name": ""}, []string{"name"}},
		{"blank name", map[string]any{"name": "   "}, []string{"name"}},
		{"name too long", map[string]any{"name": strings.Repeat("é", 101)}, []string{"name"}},
		{"unknown currency", map[string]any{"currency": "XXX"}, []string{"currency"}},
		{"real but unsupported currency", map[string]any{"currency": "CHF"}, []string{"currency"}},
		{"empty currency", map[string]any{"currency": ""}, []string{"currency"}},
		{"unknown timezone", map[string]any{"timezone": "Mars/Olympus_Mons"}, []string{"timezone"}},
		{"empty timezone", map[string]any{"timezone": ""}, []string{"timezone"}},
		{"the server's own zone", map[string]any{"timezone": "Local"}, []string{"timezone"}},
		{"an offset is not a zone", map[string]any{"timezone": "+04:00"}, []string{"timezone"}},
		{"balance too large", map[string]any{"starting_balance_minor": 1_000_000_000_001}, []string{"starting_balance_minor"}},
		{"balance too negative", map[string]any{"starting_balance_minor": -1_000_000_000_001}, []string{"starting_balance_minor"}},
		{"an upload that does not exist", map[string]any{"avatar_upload_id": uuid.NewString()}, []string{"avatar_upload_id"}},
		{"every problem is reported, and nothing is saved",
			map[string]any{"name": "Daisy", "onboarding_completed": true, "currency": "XXX", "timezone": "Nowhere"},
			[]string{"currency", "timezone"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := patchMe(t, server, user, tt.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
			body := testutil.DecodeJSON[errorBody](t, rec)
			assert.Equal(t, "validation_failed", body.Error.Code)
			fields := make([]string, 0, len(body.Error.Fields))
			for field, problem := range body.Error.Fields {
				fields = append(fields, field)
				assert.NotEmpty(t, problem)
			}
			assert.ElementsMatch(t, tt.wantFields, fields)
			assert.Equal(t, user.User, storedUser(t, pool, user), "nothing is written")
		})
	}

	t.Run("a name of exactly the limit is fine", func(t *testing.T) {
		rec := patchMe(t, server, user, map[string]any{"name": strings.Repeat("é", 100)})

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		broken := httptest.NewRequest(http.MethodPatch, "/v1/me", strings.NewReader(`{"name":`))
		broken.Header.Set("Content-Type", "application/json")
		broken.Header.Set("Authorization", "Bearer "+testutil.Token(t, user.Claims))

		rec := testutil.Do(server, broken)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("a value of the wrong type", func(t *testing.T) {
		rec := patchMe(t, server, user, map[string]any{"onboarding_completed": "yes"})

		assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	})
}

func TestUpdateMe_CurrencyLock(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	// Until transactions exist (BE-17) the check is injected: this user has some.
	var asked []uuid.UUID
	server := newServer(t, pool, users.WithTransactionCheck(func(_ context.Context, _ db.Querier, userID uuid.UUID) (bool, error) {
		asked = append(asked, userID)
		return true, nil
	}))
	user := testutil.NewTestUser(t, pool)

	t.Run("the currency cannot change", func(t *testing.T) {
		rec := patchMe(t, server, user, map[string]any{"currency": "EUR", "name": "Daisy"})

		require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		body := testutil.DecodeJSON[errorBody](t, rec)
		assert.Equal(t, "conflict", body.Error.Code)
		assert.Contains(t, body.Error.Message, "currency")
		assert.Equal(t, user.User, storedUser(t, pool, user), "the rest of the patch is not applied either")
		assert.Equal(t, []uuid.UUID{user.ID}, asked, "asked about this user")
	})

	t.Run("sending the current currency is not a change", func(t *testing.T) {
		asked = nil
		rec := patchMe(t, server, user, map[string]any{"currency": "usd"})

		assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Empty(t, asked)
	})

	t.Run("other fields still change", func(t *testing.T) {
		rec := patchMe(t, server, user, map[string]any{"timezone": "Europe/London"})

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, "Europe/London", storedUser(t, pool, user).Timezone)
	})
}

func TestUpdateMe_ChangesOnlyTheCaller(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newServer(t, pool)
	caller, other := testutil.NewTestUser(t, pool), testutil.NewTestUser(t, pool)

	rec := patchMe(t, server, caller, map[string]any{
		"name": "Changed", "currency": "JPY", "timezone": "Asia/Tokyo", "starting_balance_minor": 5000,
		"onboarding_completed": true, "preferences": map[string]any{"budget_alerts": false},
	})

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, other.User, storedUser(t, pool, other))
	assert.Equal(t, "Changed", storedUser(t, pool, caller).Name)
}

func TestUpdateMe_RequiresAToken(t *testing.T) {
	t.Parallel()
	server := newServer(t, testutil.NewDB(t))

	rec := testutil.Do(server, testutil.JSONRequest(t, http.MethodPatch, "/v1/me", map[string]any{"name": "Nobody"}))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
