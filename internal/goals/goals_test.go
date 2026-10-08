package goals_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spec "github.com/movsesmeliksetyan/sprout-api/api"
	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// now is when the tests' goals are completed.
var now = time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)

// api is the server's API with only the goals handler implemented.
type api struct {
	*goals.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

// fixture is a server with a real Postgres behind it, its clock set to now.
type fixture struct {
	pool   *pgxpool.Pool
	server http.Handler
}

func newFixture(t *testing.T, opts ...goals.Option) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	return fixture{pool: pool, server: newServer(t, pool, opts...)}
}

func newServer(t *testing.T, pool *pgxpool.Pool, opts ...goals.Option) http.Handler {
	t.Helper()
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	opts = append([]goals.Option{goals.WithClock(func() time.Time { return now })}, opts...)
	routes, err := spec.IdempotentRoutes()
	require.NoError(t, err)
	return httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
			httpx.Idempotency(pool, responder, logger, httpx.IdempotencyConfig{Routes: routes}),
		),
		httpx.WithAPI(api{Handler: goals.NewHandler(goals.NewService(pool, opts...))}),
	).Handler()
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body))
}

func (f fixture) list(t *testing.T, user testutil.TestUser) httpx.GoalList {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/goals", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.GoalList](t, rec)
}

func (f fixture) create(t *testing.T, user testutil.TestUser, body map[string]any) httpx.Goal {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, "/goals", body)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Goal](t, rec)
}

func (f fixture) get(t *testing.T, user testutil.TestUser, id uuid.UUID) httpx.GoalDetail {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/goals/"+id.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.GoalDetail](t, rec)
}

func (f fixture) patch(t *testing.T, user testutil.TestUser, id uuid.UUID, body map[string]any) httpx.Goal {
	t.Helper()
	rec := f.do(t, user, http.MethodPatch, "/goals/"+id.String(), body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Goal](t, rec)
}

// stored reads the goal's row.
func (f fixture) stored(t *testing.T, id uuid.UUID) db.Goal {
	t.Helper()
	var userID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT user_id FROM goals WHERE id = $1", id).Scan(&userID))
	row, err := db.New(f.pool).GetGoal(context.Background(), db.GetGoalParams{ID: id, UserID: userID})
	require.NoError(t, err)
	return row.Goal
}

// save tops the goal up by amount on the given day of July 2025.
func (f fixture) save(t *testing.T, user testutil.TestUser, goalID uuid.UUID, amount int64, day int) db.GoalContribution {
	t.Helper()
	return testutil.NewContribution(t, f.pool, user.User, goalID, "topup", amount, time.Date(2025, 7, day, 12, 0, 0, 0, time.UTC))
}

func fields(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	body := testutil.DecodeJSON[httpx.ErrorResponse](t, rec)
	require.NotNil(t, body.Error.Fields)
	return *body.Error.Fields
}

func titles(list httpx.GoalList) []string {
	out := make([]string, 0, len(list.Items))
	for _, goal := range list.Items {
		out = append(out, goal.Title)
	}
	return out
}

// TestListGoals_TheDesign reproduces the Goals screen of the design: $1,338
// saved of $2,169 across three goals.
func TestListGoals_TheDesign(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	labubu := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Labubu"), testutil.GoalTarget(12000))
	trip := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Trip"), testutil.GoalTarget(150000))
	bike := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Bike"), testutil.GoalTarget(54900))
	f.save(t, user, labubu.ID, 4300, 1)
	f.save(t, user, labubu.ID, 2500, 8)
	f.save(t, user, trip.ID, 120000, 2)
	testutil.NewContribution(t, f.pool, user.User, trip.ID, "withdrawal", 20000, time.Date(2025, 7, 9, 12, 0, 0, 0, time.UTC))
	f.save(t, user, bike.ID, 27000, 3)

	list := f.list(t, user)

	assert.EqualValues(t, 133800, list.TotalSavedMinor)
	assert.EqualValues(t, 216900, list.TotalTargetMinor)
	assert.Equal(t, 62, list.Pct)
	require.Equal(t, []string{"Labubu", "Trip", "Bike"}, titles(list))

	first := list.Items[0]
	assert.Equal(t, labubu.ID, first.ID)
	assert.EqualValues(t, 12000, first.TargetMinor)
	assert.EqualValues(t, 6800, first.SavedMinor)
	assert.EqualValues(t, 5200, first.RemainingMinor)
	assert.Equal(t, 57, first.Pct)
	assert.Equal(t, httpx.GoalStatus("active"), first.Status)
	assert.Equal(t, 0, first.SortOrder)

	assert.EqualValues(t, 100000, list.Items[1].SavedMinor, "a withdrawal takes from what is saved")
	assert.Equal(t, 67, list.Items[1].Pct)
	assert.Equal(t, 49, list.Items[2].Pct)
}

func TestListGoals_EmptyUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodGet, "/goals", nil)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{"total_saved_minor": 0, "total_target_minor": 0, "pct": 0, "items": []}`, rec.Body.String())
}

func TestListGoals_ShowsActiveAndCompletedInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	first := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("First"))
	done := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Done"), testutil.GoalTarget(500), testutil.GoalStatus("completed"))
	shelved := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Shelved"), testutil.GoalStatus("archived"))
	testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Last"))
	theirs := testutil.NewGoal(t, f.pool, other.ID, testutil.GoalTitle("Theirs"))
	f.save(t, user, done.ID, 900, 1)
	f.save(t, user, shelved.ID, 4000, 1)
	f.save(t, other, theirs.ID, 7000, 1)
	f.patch(t, user, first.ID, map[string]any{"sort_order": 9})

	list := f.list(t, user)

	assert.Equal(t, []string{"Done", "Last", "First"}, titles(list), "by sort order, without the archived one")
	assert.EqualValues(t, 900, list.TotalSavedMinor, "an archived goal's savings are not in the total")
	assert.EqualValues(t, 20500, list.TotalTargetMinor)
	done1 := list.Items[0]
	assert.Equal(t, httpx.GoalStatus("completed"), done1.Status)
	assert.EqualValues(t, 900, done1.SavedMinor)
	assert.Zero(t, done1.RemainingMinor, "never below zero")
	assert.Equal(t, 100, done1.Pct, "never above 100")
}

func TestCreateGoal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.do(t, user, http.MethodPost, "/goals", map[string]any{"title": "  Labubu ", "emoji": " 🧸 ", "target_minor": 12000})

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	created := testutil.DecodeJSON[httpx.Goal](t, rec)
	assert.JSONEq(t, fmt.Sprintf(`{
		"id": %q, "title": "Labubu", "emoji": "🧸", "image_url": null,
		"target_minor": 12000, "saved_minor": 0, "remaining_minor": 12000, "pct": 0,
		"monthly_pace_minor": 0, "eta_month": null, "status": "active", "sort_order": 0,
		"created_at": %q
	}`, created.ID, created.CreatedAt.Format(time.RFC3339Nano)), rec.Body.String())
	assert.Equal(t, uuid.Version(7), created.ID.Version())

	second := f.create(t, user, map[string]any{"title": "Trip", "emoji": "  ", "target_minor": 1})

	assert.Equal(t, 1, second.SortOrder, "a new goal goes last")
	assert.Nil(t, second.Emoji, "an empty emoji is none")
	assert.Equal(t, []string{"Labubu", "Trip"}, titles(f.list(t, user)))
}

func TestCreateGoal_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	tests := []struct {
		name string
		body map[string]any
		want []string
	}{
		{"empty title", map[string]any{"title": "   ", "target_minor": 100}, []string{"title"}},
		{"title too long", map[string]any{"title": strings.Repeat("é", 41), "target_minor": 100}, []string{"title"}},
		{"no target", map[string]any{"title": "Trip", "target_minor": 0}, []string{"target_minor"}},
		{"negative target", map[string]any{"title": "Trip", "target_minor": -5}, []string{"target_minor"}},
		{"target too large", map[string]any{"title": "Trip", "target_minor": 1_000_000_000_001}, []string{"target_minor"}},
		{"emoji too long", map[string]any{"title": "Trip", "emoji": strings.Repeat("x", 17), "target_minor": 100}, []string{"emoji"}},
		{"unknown upload", map[string]any{"title": "Trip", "target_minor": 100, "image_upload_id": uuid.NewString()}, []string{"image_upload_id"}},
		{"everything wrong", map[string]any{"title": "", "emoji": strings.Repeat("x", 17), "target_minor": 0}, []string{"title", "emoji", "target_minor"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fields(t, f.do(t, user, http.MethodPost, "/goals", tt.body))

			keys := make([]string, 0, len(got))
			for key := range got {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, tt.want, keys)
		})
	}

	accepted := f.create(t, user, map[string]any{"title": strings.Repeat("é", 40), "target_minor": 1_000_000_000_000})
	assert.Len(t, []rune(accepted.Title), 40)
	assert.Len(t, f.list(t, user).Items, 1, "a refused goal is not stored")

	rec := f.do(t, user, http.MethodPost, "/goals", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a request without a body")
}

func TestCreateGoal_AtMostTwentyActive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	for range goals.MaxActive - 1 {
		testutil.NewGoal(t, f.pool, user.ID)
	}
	testutil.NewGoal(t, f.pool, user.ID, testutil.GoalStatus("completed"))
	shelved := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalStatus("archived"))
	testutil.NewGoal(t, f.pool, other.ID)

	f.create(t, user, map[string]any{"title": "The twentieth", "target_minor": 100})
	rec := f.do(t, user, http.MethodPost, "/goals", map[string]any{"title": "One too many", "target_minor": 100})

	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "at most 20 active goals")

	rec = f.do(t, user, http.MethodPatch, "/goals/"+shelved.ID.String(), map[string]any{"status": "active"})
	assert.Equal(t, http.StatusConflict, rec.Code, "an archived goal cannot come back into a full list")

	f.create(t, other, map[string]any{"title": "Their own count", "target_minor": 100})
}

func TestGetGoal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Trip"), testutil.GoalTarget(100000))
	another := testutil.NewGoal(t, f.pool, user.ID)
	f.save(t, user, another.ID, 999, 20)
	for day := 1; day <= 6; day++ {
		f.save(t, user, goal.ID, int64(day*100), day)
	}
	newest := testutil.NewContribution(t, f.pool, user.User, goal.ID, "withdrawal", 50, time.Date(2025, 7, 7, 23, 30, 0, 0, time.UTC))

	detail := f.get(t, user, goal.ID)

	assert.Equal(t, "Trip", detail.Title)
	assert.EqualValues(t, 2050, detail.SavedMinor)
	assert.EqualValues(t, 97950, detail.RemainingMinor)
	assert.Equal(t, 2, detail.Pct)
	require.Len(t, detail.RecentContributions, 5, "the five newest of seven")
	first := detail.RecentContributions[0]
	assert.Equal(t, newest.ID, first.ID)
	assert.Equal(t, httpx.ContributionKind("withdrawal"), first.Kind)
	assert.EqualValues(t, 50, first.AmountMinor)
	assert.Equal(t, "2025-07-07", first.LocalDate.Format(time.DateOnly))
	assert.True(t, newest.OccurredAt.Equal(first.OccurredAt))
	assert.EqualValues(t, 300, detail.RecentContributions[4].AmountMinor, "the withdrawal, then the 6th back to the 3rd")

	rec := f.do(t, user, http.MethodGet, "/goals/"+another.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"recent_contributions":[{`)

	empty := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalStatus("archived"))
	rec = f.do(t, user, http.MethodGet, "/goals/"+empty.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, "an archived goal can still be read")
	assert.Contains(t, rec.Body.String(), `"recent_contributions":[]`)
}

func TestUpdateGoal_Fields(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	created := f.create(t, user, map[string]any{"title": "Trip", "emoji": "✈️", "target_minor": 5000})
	f.save(t, user, created.ID, 1000, 1)

	updated := f.patch(t, user, created.ID, map[string]any{"title": " Japan ", "emoji": "🗾", "target_minor": 4000, "sort_order": 3})

	assert.Equal(t, "Japan", updated.Title)
	assert.Equal(t, "🗾", *updated.Emoji)
	assert.EqualValues(t, 4000, updated.TargetMinor)
	assert.EqualValues(t, 1000, updated.SavedMinor)
	assert.EqualValues(t, 3000, updated.RemainingMinor)
	assert.Equal(t, 25, updated.Pct)
	assert.Equal(t, 3, updated.SortOrder)

	unchanged := f.patch(t, user, created.ID, map[string]any{})
	assert.Equal(t, updated, unchanged, "an empty patch changes nothing")

	cleared := f.patch(t, user, created.ID, map[string]any{"emoji": nil})
	assert.Nil(t, cleared.Emoji)
	assert.Equal(t, "Japan", cleared.Title)
}

func TestUpdateGoal_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Trip"))

	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{"empty title", map[string]any{"title": " "}, "title"},
		{"title too long", map[string]any{"title": strings.Repeat("a", 41)}, "title"},
		{"no target", map[string]any{"target_minor": 0}, "target_minor"},
		{"emoji too long", map[string]any{"emoji": strings.Repeat("x", 17)}, "emoji"},
		{"completed by hand", map[string]any{"status": "completed"}, "status"},
		{"unknown status", map[string]any{"status": "paused"}, "status"},
		{"negative sort order", map[string]any{"sort_order": -1}, "sort_order"},
		{"unknown upload", map[string]any{"image_upload_id": uuid.NewString()}, "image_upload_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.body["title"] = cmpOr(tt.body["title"], "Should Not Stick")

			got := fields(t, f.do(t, user, http.MethodPatch, "/goals/"+goal.ID.String(), tt.body))

			assert.Contains(t, got, tt.want)
			assert.Equal(t, "Trip", f.stored(t, goal.ID).Title, "nothing is written")
		})
	}
}

// cmpOr returns value unless it is nil, then fallback.
func cmpOr(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func TestUpdateGoal_Status(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Trip"), testutil.GoalTarget(5000))
	f.save(t, user, goal.ID, 3000, 1)

	archived := f.patch(t, user, goal.ID, map[string]any{"status": "archived"})

	assert.Equal(t, httpx.GoalStatus("archived"), archived.Status)
	assert.Empty(t, f.list(t, user).Items, "an archived goal leaves the list")

	// Lowering the target of an archived goal does not complete it.
	still := f.patch(t, user, goal.ID, map[string]any{"target_minor": 3000})
	assert.Equal(t, httpx.GoalStatus("archived"), still.Status)
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)

	// Brought back fully saved, it is completed.
	back := f.patch(t, user, goal.ID, map[string]any{"status": "active"})
	assert.Equal(t, httpx.GoalStatus("completed"), back.Status)
	assert.Equal(t, 100, back.Pct)
	completedAt := f.stored(t, goal.ID).CompletedAt
	require.NotNil(t, completedAt)
	assert.True(t, completedAt.Equal(now))

	// Raising the target reopens it; lowering it completes it again.
	reopened := f.patch(t, user, goal.ID, map[string]any{"target_minor": 3001})
	assert.Equal(t, httpx.GoalStatus("active"), reopened.Status)
	assert.EqualValues(t, 1, reopened.RemainingMinor)
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)

	completed := f.patch(t, user, goal.ID, map[string]any{"target_minor": 2000})
	assert.Equal(t, httpx.GoalStatus("completed"), completed.Status)
	assert.Zero(t, completed.RemainingMinor)
	assert.Equal(t, 100, completed.Pct)

	// A completed goal can be archived and keeps when it was completed.
	f.patch(t, user, goal.ID, map[string]any{"status": "archived"})
	assert.NotNil(t, f.stored(t, goal.ID).CompletedAt)
}

func TestDeleteGoal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID)
	kept := testutil.NewGoal(t, f.pool, user.ID)
	f.save(t, user, goal.ID, 100, 1)
	f.save(t, user, goal.ID, 200, 2)
	f.save(t, user, kept.ID, 300, 3)

	rec := f.do(t, user, http.MethodDelete, "/goals/"+goal.ID.String(), nil)

	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, rec.Body.String())
	var contributions int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM goal_contributions WHERE user_id = $1", user.ID).Scan(&contributions))
	assert.Equal(t, 1, contributions, "the goal's contributions go with it; another goal's stay")
	assert.Len(t, f.list(t, user).Items, 1)

	rec = f.do(t, user, http.MethodDelete, "/goals/"+goal.ID.String(), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGoals_SomeoneElsesGoalIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	theirs := testutil.NewGoal(t, f.pool, other.ID, testutil.GoalTitle("Theirs"))
	f.save(t, other, theirs.ID, 500, 1)
	unknown, err := uuid.NewV7()
	require.NoError(t, err)

	for name, id := range map[string]uuid.UUID{"someone else's": theirs.ID, "unknown": unknown} {
		path := "/goals/" + id.String()
		requests := map[string]*httptest.ResponseRecorder{
			"get":    f.do(t, owner, http.MethodGet, path, nil),
			"patch":  f.do(t, owner, http.MethodPatch, path, map[string]any{"title": "Mine now"}),
			"delete": f.do(t, owner, http.MethodDelete, path, nil),
		}
		for verb, rec := range requests {
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", verb, name, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"not_found"`)
		}
	}

	assert.Equal(t, "Theirs", f.get(t, other, theirs.ID).Title, "the goal is untouched")
	assert.Empty(t, f.list(t, owner).Items)
}

func TestGoals_RequireAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := uuid.NewString()

	for _, req := range []*http.Request{
		testutil.JSONRequest(t, http.MethodGet, "/v1/goals", nil),
		testutil.JSONRequest(t, http.MethodPost, "/v1/goals", map[string]any{"title": "Trip", "target_minor": 100}),
		testutil.JSONRequest(t, http.MethodGet, "/v1/goals/"+id, nil),
		testutil.JSONRequest(t, http.MethodPatch, "/v1/goals/"+id, map[string]any{"title": "Trip"}),
		testutil.JSONRequest(t, http.MethodDelete, "/v1/goals/"+id, nil),
	} {
		rec := testutil.Do(f.server, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", req.Method, req.URL.Path)
	}
}

// imageFixture is a goals server whose pictures live in real object storage.
type imageFixture struct {
	fixture
	store   *storage.S3
	uploads *uploads.Service
}

func newImageFixture(t *testing.T) imageFixture {
	t.Helper()
	pool, store := testutil.NewDB(t), testutil.NewStore(t)
	uploadService := uploads.NewService(pool, store)
	server := newServer(t, pool, goals.WithImages(uploadService, store, testutil.Logger(t)))
	return imageFixture{fixture: fixture{pool: pool, server: server}, store: store, uploads: uploadService}
}

// upload registers an upload for the user and sends content to it.
func (f imageFixture) upload(t *testing.T, user testutil.TestUser, purpose uploads.Purpose, content []byte) uuid.UUID {
	t.Helper()
	created, err := f.uploads.Create(context.Background(), user.ID, uploads.CreateParams{
		Purpose: purpose, ContentType: "image/png", SizeBytes: int64(len(content)),
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPut, created.URL, bytes.NewReader(content))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return created.Upload.ID
}

func (f imageFixture) objectExists(t *testing.T, key string) bool {
	t.Helper()
	_, err := f.store.Head(context.Background(), key)
	if err != nil {
		require.ErrorIs(t, err, storage.ErrNotFound)
		return false
	}
	return true
}

func download(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

func TestGoalImage_SetReplaceRemoveAndDelete(t *testing.T) {
	t.Parallel()
	f := newImageFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	first, second := []byte("first picture"), []byte("second picture")

	firstID := f.upload(t, user, uploads.PurposeGoalImage, first)
	created := f.create(t, user, map[string]any{"title": "Trip", "target_minor": 100, "image_upload_id": firstID})

	require.NotNil(t, created.ImageURL)
	status, body := download(t, *created.ImageURL)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, first, body)
	firstKey := *f.stored(t, created.ID).ImageKey
	assert.Equal(t, "u/"+user.ID.String()+"/goal_image/"+firstID.String(), firstKey)
	require.NotNil(t, f.list(t, user).Items[0].ImageURL, "the list links to the picture too")
	require.NotNil(t, f.get(t, user, created.ID).ImageURL)

	// A patch that does not mention the picture keeps it.
	renamed := f.patch(t, user, created.ID, map[string]any{"title": "Japan"})
	require.NotNil(t, renamed.ImageURL)
	assert.True(t, f.objectExists(t, firstKey))

	secondID := f.upload(t, user, uploads.PurposeGoalImage, second)
	replaced := f.patch(t, user, created.ID, map[string]any{"image_upload_id": secondID})

	require.NotNil(t, replaced.ImageURL)
	_, body = download(t, *replaced.ImageURL)
	assert.Equal(t, second, body)
	assert.False(t, f.objectExists(t, firstKey), "the replaced picture is deleted")
	secondKey := *f.stored(t, created.ID).ImageKey

	got := fields(t, f.do(t, user, http.MethodPatch, "/goals/"+created.ID.String(), map[string]any{"image_upload_id": secondID}))
	assert.Equal(t, "upload has already been used", got["image_upload_id"])
	assert.True(t, f.objectExists(t, secondKey), "a refused upload leaves the picture alone")

	removed := f.patch(t, user, created.ID, map[string]any{"image_upload_id": nil})
	assert.Nil(t, removed.ImageURL)
	assert.False(t, f.objectExists(t, secondKey))

	thirdID := f.upload(t, user, uploads.PurposeGoalImage, first)
	f.patch(t, user, created.ID, map[string]any{"image_upload_id": thirdID})
	thirdKey := *f.stored(t, created.ID).ImageKey
	rec := f.do(t, user, http.MethodDelete, "/goals/"+created.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, f.objectExists(t, thirdKey), "a deleted goal's picture is deleted")
}

func TestGoalImage_RefusedUploads(t *testing.T) {
	t.Parallel()
	f := newImageFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	avatar := f.upload(t, owner, uploads.PurposeAvatar, []byte("a face"))
	theirs := f.upload(t, other, uploads.PurposeGoalImage, []byte("their picture"))
	neverSent, err := f.uploads.Create(context.Background(), owner.ID, uploads.CreateParams{
		Purpose: uploads.PurposeGoalImage, ContentType: "image/png", SizeBytes: 10,
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		uploadID uuid.UUID
		want     string
	}{
		{"made for another purpose", avatar, "upload was made for a different purpose"},
		{"someone else's", theirs, "upload not found"},
		{"never uploaded", neverSent.Upload.ID, "the file has not been uploaded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fields(t, f.do(t, owner, http.MethodPost, "/goals",
				map[string]any{"title": "Trip", "target_minor": 100, "image_upload_id": tt.uploadID}))

			assert.Equal(t, tt.want, got["image_upload_id"])
		})
	}
	assert.Empty(t, f.list(t, owner).Items, "a goal with a refused picture is not created")

	// The owner of the upload can still use it.
	created := f.create(t, other, map[string]any{"title": "Theirs", "target_minor": 100, "image_upload_id": theirs})
	assert.NotNil(t, created.ImageURL)
}
