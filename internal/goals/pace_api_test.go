package goals_test

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

// createdAt moves the day the goal was created on.
func (f fixture) createdAt(t *testing.T, goalID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE goals SET created_at = $1 WHERE id = $2", at, goalID)
	require.NoError(t, err)
}

// contributed stores a contribution to the goal at midday UTC that many days
// before now.
func (f fixture) contributed(t *testing.T, user testutil.TestUser, goalID uuid.UUID, kind string, amount int64, daysAgo int) {
	t.Helper()
	testutil.NewContribution(t, f.pool, user.User, goalID, kind, amount,
		time.Date(now.Year(), now.Month(), now.Day()-daysAgo, 12, 0, 0, 0, time.UTC))
}

func TestGoals_PaceAndETA(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	lastYear := now.AddDate(-1, 0, 0)

	// 7,500 net in the last 90 days: 2,500 a month. 8,100 is saved, the
	// older 600 included, which leaves 5,200.
	steady := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Steady"), testutil.GoalTarget(13300))
	f.createdAt(t, steady.ID, lastYear)
	f.contributed(t, user, steady.ID, "topup", 4000, 0)
	f.contributed(t, user, steady.ID, "topup", 4500, 89)
	f.contributed(t, user, steady.ID, "withdrawal", 1000, 30)
	f.contributed(t, user, steady.ID, "topup", 600, 90) // a day too old to count
	f.contributed(t, user, steady.ID, "topup", 9999, -1)
	f.contributed(t, user, steady.ID, "withdrawal", 9999, -1) // tomorrow's do not count yet

	stalled := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Stalled"))
	f.createdAt(t, stalled.ID, lastYear)
	f.contributed(t, user, stalled.ID, "topup", 3000, 120)

	draining := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Draining"))
	f.createdAt(t, draining.ID, lastYear)
	f.contributed(t, user, draining.ID, "topup", 6000, 200)
	f.contributed(t, user, draining.ID, "withdrawal", 3000, 10)

	done := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Done"), testutil.GoalTarget(900), testutil.GoalStatus("completed"))
	f.createdAt(t, done.ID, lastYear)
	f.contributed(t, user, done.ID, "topup", 900, 5)

	fresh := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Fresh"))
	f.createdAt(t, fresh.ID, now)

	type figures struct {
		Pace int64
		ETA  string
	}
	got := map[string]figures{}
	for _, goal := range f.list(t, user).Items {
		eta := ""
		if goal.EtaMonth != nil {
			eta = *goal.EtaMonth
		}
		got[goal.Title] = figures{goal.MonthlyPaceMinor, eta}
	}
	assert.Equal(t, map[string]figures{
		"Steady":   {2500, "2025-10"},
		"Stalled":  {0, ""},
		"Draining": {-1000, ""},
		"Done":     {300, ""},
		"Fresh":    {0, ""},
	}, got)

	detail := f.get(t, user, steady.ID)
	assert.EqualValues(t, 2500, detail.MonthlyPaceMinor)
	require.NotNil(t, detail.EtaMonth)
	assert.Equal(t, "2025-10", *detail.EtaMonth)
	rec := f.do(t, user, http.MethodGet, "/goals/"+stalled.ID.String(), nil)
	assert.Contains(t, rec.Body.String(), `"monthly_pace_minor":0`)
	assert.Contains(t, rec.Body.String(), `"eta_month":null`)

	// Every answer that holds the goal has the figures as they are then.
	first := f.contribute(t, user, fresh.ID, "topup", 2500)
	assert.EqualValues(t, 2500, first.Goal.MonthlyPaceMinor, "a goal younger than a month counts as a month old")
	require.NotNil(t, first.Goal.EtaMonth)
	assert.Equal(t, "2025-10", *first.Goal.EtaMonth, "7,500 to go")
	patched := f.patch(t, user, fresh.ID, map[string]any{"target_minor": 5000})
	require.NotNil(t, patched.EtaMonth)
	assert.Equal(t, "2025-08", *patched.EtaMonth)
	removed := f.remove(t, user, fresh.ID, first.Contribution.ID)
	assert.Zero(t, removed.MonthlyPaceMinor)
	assert.Nil(t, removed.EtaMonth)
}

func TestGoals_PaceCountsDaysInTheUsersTimezone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)
	// now is 21 July 17:24 UTC, which in Auckland is already the 22nd: the
	// window there starts a day later, on 24 April.
	timezone := "Pacific/Auckland"
	updated, err := db.New(f.pool).UpdateUser(context.Background(), db.UpdateUserParams{ID: user.ID, Timezone: &timezone})
	require.NoError(t, err)
	user.User = updated
	goal := testutil.NewGoal(t, f.pool, user.ID)
	f.createdAt(t, goal.ID, now.AddDate(-1, 0, 0))
	testutil.NewContribution(t, f.pool, user.User, goal.ID, "topup", 3000, time.Date(2025, 4, 23, 11, 0, 0, 0, time.UTC)) // the 23rd there
	testutil.NewContribution(t, f.pool, user.User, goal.ID, "topup", 600, time.Date(2025, 4, 23, 13, 0, 0, 0, time.UTC))  // the 24th there
	theirs := testutil.NewGoal(t, f.pool, other.ID)
	f.contributed(t, other, theirs.ID, "topup", 9000, 1)

	got := f.get(t, user, goal.ID)

	assert.EqualValues(t, 200, got.MonthlyPaceMinor, "only the contribution of the 24th is in the window")
	assert.Equal(t, httpx.GoalStatus("active"), got.Status)
	require.Len(t, f.list(t, user).Items, 1)
	assert.EqualValues(t, 200, f.list(t, user).Items[0].MonthlyPaceMinor, "someone else's contributions are not counted")
}
