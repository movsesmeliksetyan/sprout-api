package goals_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func contributionsPath(goalID uuid.UUID) string {
	return "/goals/" + goalID.String() + "/contributions"
}

// contribute posts a contribution to the goal.
func (f fixture) contribute(t *testing.T, user testutil.TestUser, goalID uuid.UUID, kind string, amount int64) httpx.ContributionCreated {
	t.Helper()
	rec := f.do(t, user, http.MethodPost, contributionsPath(goalID), map[string]any{"kind": kind, "amount_minor": amount})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.ContributionCreated](t, rec)
}

// remove deletes a contribution of the goal.
func (f fixture) remove(t *testing.T, user testutil.TestUser, goalID, id uuid.UUID) httpx.Goal {
	t.Helper()
	rec := f.do(t, user, http.MethodDelete, contributionsPath(goalID)+"/"+id.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Goal](t, rec)
}

func (f fixture) page(t *testing.T, user testutil.TestUser, goalID uuid.UUID, query string) httpx.ContributionList {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, contributionsPath(goalID)+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.ContributionList](t, rec)
}

func (f fixture) contributions(t *testing.T, goalID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM goal_contributions WHERE goal_id = $1", goalID).Scan(&count))
	return count
}

func TestCreateContribution(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTarget(12000))

	topup := f.contribute(t, user, goal.ID, "topup", 6800)

	assert.Equal(t, httpx.ContributionKind("topup"), topup.Contribution.Kind)
	assert.EqualValues(t, 6800, topup.Contribution.AmountMinor)
	assert.True(t, now.Equal(topup.Contribution.OccurredAt), "without occurred_at it is now")
	assert.Equal(t, "2025-07-21", topup.Contribution.LocalDate.Format(time.DateOnly))
	assert.Equal(t, goal.ID, topup.Goal.ID)
	assert.EqualValues(t, 6800, topup.Goal.SavedMinor)
	assert.EqualValues(t, 5200, topup.Goal.RemainingMinor)
	assert.Equal(t, 57, topup.Goal.Pct)
	assert.Equal(t, httpx.GoalStatus("active"), topup.Goal.Status)

	withdrawal := f.contribute(t, user, goal.ID, "withdrawal", 800)

	assert.Equal(t, httpx.ContributionKind("withdrawal"), withdrawal.Contribution.Kind)
	assert.EqualValues(t, 6000, withdrawal.Goal.SavedMinor)
	detail := f.get(t, user, goal.ID)
	assert.EqualValues(t, 6000, detail.SavedMinor)
	require.Len(t, detail.RecentContributions, 2)
	assert.Equal(t, withdrawal.Contribution.ID, detail.RecentContributions[0].ID)
}

func TestCreateContribution_LocalDateFollowsTheUsersTimezone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	timezone := "Asia/Yerevan"
	_, err := db.New(f.pool).UpdateUser(context.Background(), db.UpdateUserParams{ID: user.ID, Timezone: &timezone})
	require.NoError(t, err)
	goal := testutil.NewGoal(t, f.pool, user.ID)

	rec := f.do(t, user, http.MethodPost, contributionsPath(goal.ID),
		map[string]any{"kind": "topup", "amount_minor": 100, "occurred_at": "2025-07-01T21:30:00Z"})

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	created := testutil.DecodeJSON[httpx.ContributionCreated](t, rec)
	assert.True(t, time.Date(2025, 7, 1, 21, 30, 0, 0, time.UTC).Equal(created.Contribution.OccurredAt))
	assert.Equal(t, "2025-07-02", created.Contribution.LocalDate.Format(time.DateOnly), "half past one at night in Yerevan")
}

func TestCreateContribution_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID)
	f.save(t, user, goal.ID, 500, 1)

	tests := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"unknown kind", map[string]any{"kind": "gift", "amount_minor": 100}, "kind"},
		{"no amount", map[string]any{"kind": "topup", "amount_minor": 0}, "amount_minor"},
		{"negative amount", map[string]any{"kind": "topup", "amount_minor": -5}, "amount_minor"},
		{"amount out of range", map[string]any{"kind": "topup", "amount_minor": 1_000_000_000_001}, "amount_minor"},
		{"withdrawal of more than is saved", map[string]any{"kind": "withdrawal", "amount_minor": 501}, "amount_minor"},
	}
	for _, tt := range tests {
		got := fields(t, f.do(t, user, http.MethodPost, contributionsPath(goal.ID), tt.body))
		assert.Contains(t, got, tt.field, tt.name)
	}
	both := fields(t, f.do(t, user, http.MethodPost, contributionsPath(goal.ID), map[string]any{"kind": "", "amount_minor": 0}))
	assert.Len(t, both, 2, "every invalid field is reported")
	assert.Equal(t, 1, f.contributions(t, goal.ID), "nothing was stored")

	all := f.contribute(t, user, goal.ID, "withdrawal", 500)
	assert.Zero(t, all.Goal.SavedMinor, "everything saved can be withdrawn")

	rec := f.do(t, user, http.MethodPost, contributionsPath(goal.ID), nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "no body")
}

func TestCreateContribution_Idempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID)
	key := uuid.NewString()
	post := func(amount int) *httptest.ResponseRecorder {
		req := testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1"+contributionsPath(goal.ID),
			map[string]any{"kind": "topup", "amount_minor": amount})
		req.Header.Set("Idempotency-Key", key)
		return testutil.Do(f.server, req)
	}

	first := post(2500)
	second := post(2500)
	different := post(2501)

	require.Equal(t, http.StatusCreated, first.Code, "body: %s", first.Body.String())
	require.Equal(t, http.StatusCreated, second.Code, "body: %s", second.Body.String())
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.Equal(t, "true", second.Header().Get("Idempotency-Replayed"))
	assert.Equal(t, http.StatusConflict, different.Code)
	assert.Equal(t, 1, f.contributions(t, goal.ID))
	assert.EqualValues(t, 2500, f.get(t, user, goal.ID).SavedMinor)
}

func TestContributions_CompleteAndReopenTheGoal(t *testing.T) {
	t.Parallel()
	var completed []uuid.UUID
	f := newFixture(t, goals.WithCompletionHook(func(_ context.Context, _ db.Querier, goal db.Goal) error {
		completed = append(completed, goal.ID)
		return nil
	}))
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTarget(1000))

	assert.Equal(t, httpx.GoalStatus("active"), f.contribute(t, user, goal.ID, "topup", 999).Goal.Status)
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)
	assert.Empty(t, completed)

	reached := f.contribute(t, user, goal.ID, "topup", 1)
	assert.Equal(t, httpx.GoalStatus("completed"), reached.Goal.Status, "saved has reached the target")
	assert.Equal(t, 100, reached.Goal.Pct)
	stored := f.stored(t, goal.ID)
	require.NotNil(t, stored.CompletedAt)
	assert.True(t, now.Equal(*stored.CompletedAt))
	assert.Equal(t, []uuid.UUID{goal.ID}, completed)

	over := f.contribute(t, user, goal.ID, "topup", 500)
	assert.Equal(t, httpx.GoalStatus("completed"), over.Goal.Status)
	assert.Zero(t, over.Goal.RemainingMinor)
	assert.Equal(t, 100, over.Goal.Pct)
	assert.Len(t, completed, 1, "a completed goal does not complete again")

	withdrawn := f.contribute(t, user, goal.ID, "withdrawal", 501)
	assert.Equal(t, httpx.GoalStatus("active"), withdrawn.Goal.Status, "a withdrawal took it below the target")
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)

	again := f.contribute(t, user, goal.ID, "topup", 1)
	assert.Equal(t, httpx.GoalStatus("completed"), again.Goal.Status)
	assert.Len(t, completed, 2)

	reopened := f.remove(t, user, goal.ID, again.Contribution.ID)
	assert.Equal(t, httpx.GoalStatus("active"), reopened.Status, "so did deleting a top-up")
	assert.EqualValues(t, 999, reopened.SavedMinor)
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)

	finished := f.remove(t, user, goal.ID, withdrawn.Contribution.ID)
	assert.Equal(t, httpx.GoalStatus("completed"), finished.Status, "deleting a withdrawal brings it back")
	assert.Len(t, completed, 3)

	f.patch(t, user, goal.ID, map[string]any{"target_minor": 5000})
	f.patch(t, user, goal.ID, map[string]any{"target_minor": 1500})
	assert.Len(t, completed, 4, "a lowered target completes it as a top-up does")
}

func TestContributions_AnArchivedGoalStaysArchived(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTarget(1000), testutil.GoalStatus("archived"))

	topup := f.contribute(t, user, goal.ID, "topup", 1000)

	assert.Equal(t, httpx.GoalStatus("archived"), topup.Goal.Status)
	assert.EqualValues(t, 1000, topup.Goal.SavedMinor)
	assert.Nil(t, f.stored(t, goal.ID).CompletedAt)
	withdrawal := f.contribute(t, user, goal.ID, "withdrawal", 400)
	assert.Equal(t, httpx.GoalStatus("archived"), withdrawal.Goal.Status)
	assert.Equal(t, httpx.GoalStatus("archived"), f.remove(t, user, goal.ID, withdrawal.Contribution.ID).Status)

	restored := f.patch(t, user, goal.ID, map[string]any{"status": "active"})
	assert.Equal(t, httpx.GoalStatus("completed"), restored.Status, "brought back, it is fully saved")
}

func TestDeleteContribution(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID)
	another := testutil.NewGoal(t, f.pool, user.ID)
	first := f.save(t, user, goal.ID, 300, 1)
	second := f.save(t, user, goal.ID, 200, 2)
	elsewhere := f.save(t, user, another.ID, 700, 3)

	after := f.remove(t, user, goal.ID, second.ID)

	assert.Equal(t, goal.ID, after.ID)
	assert.EqualValues(t, 300, after.SavedMinor)
	assert.Equal(t, 3, after.Pct)
	assert.Equal(t, 1, f.contributions(t, goal.ID))

	path := contributionsPath(goal.ID) + "/"
	rec := f.do(t, user, http.MethodDelete, path+second.ID.String(), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "it is gone")
	rec = f.do(t, user, http.MethodDelete, path+elsewhere.ID.String(), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "a contribution of another goal")
	assert.Equal(t, 1, f.contributions(t, another.ID))

	// 300 in and 250 out: without the top-up the goal would hold -250.
	f.contribute(t, user, goal.ID, "withdrawal", 250)
	rec = f.do(t, user, http.MethodDelete, path+first.ID.String(), nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"conflict"`)
	assert.Equal(t, 2, f.contributions(t, goal.ID), "the top-up is still there")
	assert.EqualValues(t, 50, f.get(t, user, goal.ID).SavedMinor)
}

func TestListContributions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	goal := testutil.NewGoal(t, f.pool, user.ID)
	another := testutil.NewGoal(t, f.pool, user.ID)
	f.save(t, user, another.ID, 999, 9)
	// Two on the 3rd, stored before the ones of the 1st and the 2nd.
	want := []int64{400, 300, 500, 200, 100}
	f.save(t, user, goal.ID, 300, 3)
	f.save(t, user, goal.ID, 400, 3)
	f.save(t, user, goal.ID, 100, 1)
	f.save(t, user, goal.ID, 200, 2)
	testutil.NewContribution(t, f.pool, user.User, goal.ID, "withdrawal", 500, time.Date(2025, 7, 2, 18, 0, 0, 0, time.UTC))

	var got []int64
	query := "?limit=2"
	for pages := 1; ; pages++ {
		page := f.page(t, user, goal.ID, query)
		for _, item := range page.Items {
			got = append(got, item.AmountMinor)
		}
		if page.NextCursor == nil {
			assert.Equal(t, 3, pages)
			break
		}
		require.Len(t, page.Items, 2)
		query = "?limit=2&cursor=" + *page.NextCursor
	}
	assert.Equal(t, want, got, "by day and then by id, newest first")

	all := f.page(t, user, goal.ID, "")
	assert.Len(t, all.Items, 5)
	assert.Nil(t, all.NextCursor)
	assert.Equal(t, httpx.ContributionKind("withdrawal"), all.Items[2].Kind)
	assert.Equal(t, "2025-07-02", all.Items[2].LocalDate.Format(time.DateOnly))

	empty := testutil.NewGoal(t, f.pool, user.ID)
	rec := f.do(t, user, http.MethodGet, contributionsPath(empty.ID), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"items":[],"next_cursor":null}`, rec.Body.String())

	for _, query := range []string{"?limit=0", "?limit=201", "?cursor=nonsense"} {
		rec := f.do(t, user, http.MethodGet, contributionsPath(goal.ID)+query, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", query, rec.Body.String())
	}
}

func TestContributions_SomeoneElsesGoalIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	theirs := testutil.NewGoal(t, f.pool, other.ID)
	contribution := f.save(t, other, theirs.ID, 500, 1)
	mine := testutil.NewGoal(t, f.pool, owner.ID)
	unknown, err := uuid.NewV7()
	require.NoError(t, err)

	for name, id := range map[string]uuid.UUID{"someone else's": theirs.ID, "unknown": unknown} {
		path := contributionsPath(id)
		requests := map[string]*httptest.ResponseRecorder{
			"list":   f.do(t, owner, http.MethodGet, path, nil),
			"create": f.do(t, owner, http.MethodPost, path, map[string]any{"kind": "topup", "amount_minor": 100}),
			"delete": f.do(t, owner, http.MethodDelete, path+"/"+contribution.ID.String(), nil),
		}
		for verb, rec := range requests {
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", verb, name, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"code":"not_found"`)
		}
	}
	rec := f.do(t, owner, http.MethodDelete, contributionsPath(mine.ID)+"/"+contribution.ID.String(), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "someone else's contribution under a goal of one's own")

	assert.Equal(t, 1, f.contributions(t, theirs.ID), "the goal is untouched")
	assert.EqualValues(t, 500, f.get(t, other, theirs.ID).SavedMinor)
}

func TestContributions_RequireAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := fmt.Sprintf("/v1/goals/%s/contributions", uuid.NewString())

	for _, req := range []*http.Request{
		testutil.JSONRequest(t, http.MethodGet, path, nil),
		testutil.JSONRequest(t, http.MethodPost, path, map[string]any{"kind": "topup", "amount_minor": 100}),
		testutil.JSONRequest(t, http.MethodDelete, path+"/"+uuid.NewString(), nil),
	} {
		rec := testutil.Do(f.server, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", req.Method, req.URL.Path)
	}
}
