package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// inZone returns a new user living in the timezone.
func (f fixture) inZone(t *testing.T, zone string) db.User {
	t.Helper()
	user := testutil.NewTestUser(t, f.pool)
	f.exec(t, `UPDATE users SET timezone = $2 WHERE id = $1`, user.ID, zone)
	reloaded, err := db.New(f.pool).GetUser(context.Background(), user.ID)
	require.NoError(t, err)
	return reloaded
}

func (f fixture) spendOn(t *testing.T, user db.User, category db.Category, minor int64, at time.Time) {
	t.Helper()
	testutil.NewTransaction(t, f.pool, user, testutil.TransactionExpense(category.ID), testutil.TransactionAmount(minor), testutil.TransactionAt(at))
}

// TestWeeklyRecaps_TimeTravel runs the hourly job, twice an hour, through
// ten days and checks that each user, in three timezones, gets one recap a
// week, on Monday morning their time.
func TestWeeklyRecaps_TimeTravel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	zones := []string{"America/New_York", "Europe/London", "Asia/Kolkata"}
	users := map[string]db.User{}
	for _, zone := range zones {
		user := f.inZone(t, zone)
		category := testutil.NewCategory(t, f.pool, user.ID)
		// A transaction in each of the two weeks recapped: 7–13 and 14–20 July.
		f.spendOn(t, user, category, 1200, time.Date(2025, 7, 9, 12, 0, 0, 0, time.UTC))
		f.spendOn(t, user, category, 3400, time.Date(2025, 7, 16, 12, 0, 0, 0, time.UTC))
		users[zone] = user
	}
	off := f.inZone(t, "Europe/London")
	f.exec(t, `UPDATE users SET weekly_recap = false WHERE id = $1`, off.ID)
	f.spendOn(t, off, testutil.NewCategory(t, f.pool, off.ID), 500, time.Date(2025, 7, 9, 12, 0, 0, 0, time.UTC))
	quiet := f.inZone(t, "Europe/London")

	seen := map[string]int{}
	for at := time.Date(2025, 7, 12, 0, 1, 0, 0, time.UTC); at.Before(time.Date(2025, 7, 23, 0, 0, 0, 0, time.UTC)); at = at.Add(time.Hour) {
		for range 2 {
			_, err := f.service.WeeklyRecaps(ctx, at)
			require.NoError(t, err)
		}
		for zone, user := range users {
			inbox := f.inbox(t, testutil.TestUser{User: user})
			if len(inbox) == seen[zone] {
				continue
			}
			require.Len(t, inbox, seen[zone]+1, "%s got two at %s", zone, at)
			seen[zone]++
			local := at.In(mustZone(t, zone))
			assert.Equal(t, time.Monday, local.Weekday(), "%s at %s", zone, at)
			assert.Equal(t, 9, local.Hour(), "%s at %s", zone, at)
		}
	}

	for _, zone := range zones {
		inbox := f.inbox(t, testutil.TestUser{User: users[zone]})
		require.Len(t, inbox, 2, "one a week in %s", zone)
		assert.Equal(t, "weekly_recap:2025-07-14", inbox[0].DedupKey)
		assert.Equal(t, "weekly_recap:2025-07-07", inbox[1].DedupKey)
		assert.Equal(t, "Your week in review", inbox[0].Title)
		assert.Equal(t, "sprout://stats", inbox[0].DeepLink)
	}
	assert.Empty(t, f.inbox(t, testutil.TestUser{User: off}), "the recap is off")
	assert.Empty(t, f.inbox(t, testutil.TestUser{User: quiet}), "no transaction last week")
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func TestWeeklyRecaps_Body(t *testing.T) {
	t.Parallel()
	monday := time.Date(2025, 7, 21, 8, 30, 0, 0, time.UTC) // 09:30 in London
	for _, tc := range []struct {
		name     string
		lastWeek map[string]int64
		before   int64
		income   bool
		want     string
	}{
		{"less", map[string]int64{"Food": 9600, "Bills": 8800}, 21000, false,
			"You spent $184 last week, 12% less than the week before. Food took the most: $96."},
		{"more", map[string]int64{"Food": 5000}, 4000, false, "You spent $50 last week, 25% more than the week before. Food took the most: $50."},
		{"nothing before", map[string]int64{"Food": 5000}, 0, false, "You spent $50 last week. Food took the most: $50."},
		{"income only", nil, 3000, true, "You spent nothing last week."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			user := f.inZone(t, "Europe/London")
			for name, minor := range tc.lastWeek {
				f.spendOn(t, user, testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName(name)), minor, time.Date(2025, 7, 16, 12, 0, 0, 0, time.UTC))
			}
			if tc.before > 0 {
				f.spendOn(t, user, testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Earlier")), tc.before, time.Date(2025, 7, 9, 12, 0, 0, 0, time.UTC))
			}
			if tc.income {
				testutil.NewTransaction(t, f.pool, user, testutil.TransactionAmount(100000), testutil.TransactionAt(time.Date(2025, 7, 15, 12, 0, 0, 0, time.UTC)))
			}
			result, err := f.service.WeeklyRecaps(context.Background(), monday)
			require.NoError(t, err)
			assert.Equal(t, 1, result.Sent)
			inbox := f.inbox(t, testutil.TestUser{User: user})
			require.Len(t, inbox, 1)
			assert.Equal(t, tc.want, inbox[0].Body)
		})
	}
}
