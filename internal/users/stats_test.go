package users_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// statsNow is the moment the stats tests look from: the afternoon of
// Monday 21 July 2025, UTC.
var statsNow = time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)

func newStatsServer(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	return newServer(t, pool, users.WithClock(func() time.Time { return statsNow }))
}

func getStats(t *testing.T, server http.Handler, user testutil.TestUser) httpx.ProfileStats {
	t.Helper()
	rec := testutil.Do(server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/me", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Me](t, rec).Stats
}

// addedAt stores a transaction the user added at the given moment. When it
// happened is another matter: a streak counts the days of adding.
func addedAt(t *testing.T, pool *pgxpool.Pool, user testutil.TestUser, createdAt time.Time) {
	t.Helper()
	transaction := testutil.NewTransaction(t, pool, user.User,
		testutil.TransactionAt(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)))
	_, err := pool.Exec(context.Background(),
		"UPDATE transactions SET created_at = $1 WHERE id = $2", createdAt, transaction.ID)
	require.NoError(t, err)
}

// daysAgo is midday UTC that many days before statsNow's day.
func daysAgo(n int) time.Time {
	return time.Date(2025, 7, 21-n, 12, 0, 0, 0, time.UTC)
}

func TestStats_Streak(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// added lists, in days before today, when transactions were added.
		added []int
		want  int
	}{
		{"no activity", nil, 0},
		{"today only", []int{0}, 1},
		{"today and the days before", []int{0, 1, 2, 3}, 4},
		{"a gap yesterday resets it", []int{0, 2, 3, 4}, 1},
		{"yesterday but not yet today still counts", []int{1, 2, 3}, 3},
		{"yesterday only", []int{1}, 1},
		{"nothing since the day before yesterday", []int{2, 3, 4}, 0},
		{"several on one day count once", []int{0, 0, 0, 1, 1}, 2},
		{"only the latest run counts", []int{0, 1, 3, 4, 5, 6, 7}, 2},
		{"across a month boundary", []int{18, 19, 20, 21, 22, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}, 23},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testutil.NewDB(t)
			server := newStatsServer(t, pool)
			user := testutil.NewTestUser(t, pool)
			for _, n := range tt.added {
				addedAt(t, pool, user, daysAgo(n))
			}

			assert.Equal(t, tt.want, getStats(t, server, user).StreakDays)
		})
	}
}

func TestStats_StreakCountsDaysInTheUsersTimezone(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newStatsServer(t, pool)
	user := testutil.NewTestUser(t, pool)
	// Both on 19 July in UTC; in Yerevan (UTC+4) the second is on the 20th.
	addedAt(t, pool, user, time.Date(2025, 7, 19, 10, 0, 0, 0, time.UTC))
	addedAt(t, pool, user, time.Date(2025, 7, 19, 21, 0, 0, 0, time.UTC))

	assert.Zero(t, getStats(t, server, user).StreakDays, "in UTC nothing was added yesterday")

	timezone := "Asia/Yerevan"
	_, err := db.New(pool).UpdateUser(context.Background(), db.UpdateUserParams{ID: user.ID, Timezone: &timezone})
	require.NoError(t, err)

	assert.Equal(t, 2, getStats(t, server, user).StreakDays, "in Yerevan the 19th and the 20th, yesterday, have one each")
}

func TestStats_StreakIgnoresOtherUsers(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newStatsServer(t, pool)
	user := testutil.NewTestUser(t, pool)
	other := testutil.NewTestUser(t, pool)
	addedAt(t, pool, user, daysAgo(0))
	addedAt(t, pool, other, daysAgo(1))
	addedAt(t, pool, other, daysAgo(2))

	assert.Equal(t, 1, getStats(t, server, user).StreakDays)
	assert.Equal(t, 2, getStats(t, server, other).StreakDays)
}

func TestStats_Counts(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newStatsServer(t, pool)
	user := testutil.NewTestUser(t, pool)
	other := testutil.NewTestUser(t, pool)
	testutil.NewCategory(t, pool, user.ID)
	testutil.NewCategory(t, pool, user.ID)
	testutil.NewCategory(t, pool, user.ID, testutil.CategoryArchived())
	testutil.NewCategory(t, pool, other.ID)
	testutil.NewGoal(t, pool, user.ID)
	testutil.NewGoal(t, pool, user.ID, testutil.GoalStatus("completed"))
	testutil.NewGoal(t, pool, user.ID, testutil.GoalStatus("archived"))
	testutil.NewGoal(t, pool, other.ID)

	stats := getStats(t, server, user)

	assert.Equal(t, 2, stats.CategoriesCount, "the active ones, and only the user's")
	assert.Equal(t, 2, stats.GoalsCount, "the active and the completed ones, and only the user's")
}

func TestStats_AreInTheAnswerToAnUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	server := newStatsServer(t, pool)
	user := testutil.NewTestUser(t, pool)
	testutil.NewCategory(t, pool, user.ID)
	addedAt(t, pool, user, daysAgo(0))

	rec := patchMe(t, server, user, map[string]any{"name": "Daisy"})

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, httpx.ProfileStats{CategoriesCount: 1, StreakDays: 1}, testutil.DecodeJSON[httpx.Me](t, rec).Stats)
}
