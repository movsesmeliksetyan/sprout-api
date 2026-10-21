package notify_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// queue stands in for the job queue, keeping the pushes enqueued.
type queue struct {
	mu   sync.Mutex
	jobs []uuid.UUID
}

func (q *queue) EnqueuePush(_ context.Context, _ pgx.Tx, _, notificationID uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, notificationID)
	return nil
}

func (q *queue) enqueued() []uuid.UUID {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]uuid.UUID(nil), q.jobs...)
}

func recap(key string) notify.Notification {
	return notify.Notification{Kind: notify.KindWeeklyRecap, Title: "Your week", Body: "You spent $120 this week.", DeepLink: "sprout://stats", DedupKey: key}
}

func (f fixture) register(t *testing.T, user testutil.TestUser, tok string, env notify.Environment) db.Device {
	t.Helper()
	device, err := f.service.RegisterDevice(context.Background(), user.ID, uuid.New(), notify.Registration{APNsToken: tok, Environment: env})
	require.NoError(t, err)
	return device
}

func (f fixture) inbox(t *testing.T, user testutil.TestUser) []db.Notification {
	t.Helper()
	page, err := f.service.List(context.Background(), user.ID, nil, nil)
	require.NoError(t, err)
	return page.Items
}

func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func TestSend_DeliversToEveryValidDevice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := testutil.NewTestUser(t, f.pool)
	phone := f.register(t, user, token(1), notify.EnvironmentProduction)
	f.register(t, user, token(2), notify.EnvironmentSandbox)
	gone := f.register(t, user, token(3), notify.EnvironmentProduction)
	f.exec(t, `UPDATE devices SET invalidated_at = now() WHERE id = $1`, gone.ID)

	created, err := f.service.Send(ctx, user.ID, recap("recap:2025-W29"))
	require.NoError(t, err)
	assert.True(t, created)
	inbox := f.inbox(t, user)
	require.Len(t, inbox, 1)
	n := inbox[0]
	assert.Equal(t, []uuid.UUID{n.ID}, f.queue.enqueued())
	assert.Nil(t, n.SentAt)

	d, err := f.service.Deliver(ctx, user.ID, n.ID)
	require.NoError(t, err)
	assert.Equal(t, notify.Delivery{Delivered: 2}, d)
	pushed := f.apns.Pushed()
	require.Len(t, pushed, 2, "the invalidated device is skipped")
	assert.Equal(t, notify.Push{
		Token: phone.ApnsToken, Environment: notify.EnvironmentProduction, Title: "Your week", Body: "You spent $120 this week.",
		NotificationID: n.ID.String(), DeepLink: "sprout://stats",
	}, pushed[0])
	assert.Equal(t, notify.EnvironmentSandbox, pushed[1].Environment)
	assert.NotNil(t, f.inbox(t, user)[0].SentAt)

	d, err = f.service.Deliver(ctx, user.ID, n.ID)
	require.NoError(t, err)
	assert.Zero(t, d, "a retried job does not push twice")
	assert.Len(t, f.apns.Pushed(), 2)

	stranger := testutil.NewTestUser(t, f.pool)
	d, err = f.service.Deliver(ctx, stranger.ID, n.ID)
	require.NoError(t, err)
	assert.Zero(t, d, "someone else's notification is nothing to deliver")
}

func TestDeliver_InvalidatesRejectedTokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := testutil.NewTestUser(t, f.pool)
	stale := f.register(t, user, token(4), notify.EnvironmentProduction)
	f.register(t, user, token(5), notify.EnvironmentProduction)
	f.apns.Invalid = map[string]bool{token(4): true}

	_, err := f.service.Send(ctx, user.ID, recap("recap:1"))
	require.NoError(t, err)
	d, err := f.service.Deliver(ctx, user.ID, f.inbox(t, user)[0].ID)
	require.NoError(t, err)
	assert.Equal(t, notify.Delivery{Delivered: 1, Invalidated: 1}, d)

	devices := f.devices(t, user)
	require.Len(t, devices, 2)
	for _, device := range devices {
		if device.ID == stale.ID {
			assert.NotNil(t, device.InvalidatedAt, "APNs rejected it")
		} else {
			assert.Nil(t, device.InvalidatedAt)
		}
	}

	_, err = f.service.Send(ctx, user.ID, recap("recap:2"))
	require.NoError(t, err)
	_, err = f.service.Deliver(ctx, user.ID, f.inbox(t, user)[0].ID)
	require.NoError(t, err)
	for _, p := range f.apns.Pushed() {
		assert.Equal(t, token(5), p.Token, "the rejected token is not tried again")
	}
}

func TestDeliver_RetriesOnlyWhenNothingArrived(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := testutil.NewTestUser(t, f.pool)
	f.register(t, user, token(6), notify.EnvironmentProduction)
	f.apns.Failing = map[string]error{token(6): errors.New("apns: 503 ServiceUnavailable")}

	_, err := f.service.Send(ctx, user.ID, recap("recap:1"))
	require.NoError(t, err)
	id := f.inbox(t, user)[0].ID
	_, err = f.service.Deliver(ctx, user.ID, id)
	require.Error(t, err, "the job tries again")
	assert.Nil(t, f.inbox(t, user)[0].SentAt)

	f.register(t, user, token(7), notify.EnvironmentProduction)
	d, err := f.service.Deliver(ctx, user.ID, id)
	require.NoError(t, err, "one device took it")
	assert.Equal(t, notify.Delivery{Delivered: 1, Failed: 1}, d)
	assert.NotNil(t, f.inbox(t, user)[0].SentAt)

	nobody := testutil.NewTestUser(t, f.pool)
	_, err = f.service.Send(ctx, nobody.ID, recap("recap:1"))
	require.NoError(t, err)
	d, err = f.service.Deliver(ctx, nobody.ID, f.inbox(t, nobody)[0].ID)
	require.NoError(t, err, "no devices is nothing to retry")
	assert.Zero(t, d)
}

func TestSend_Preferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	alert := notify.Notification{Kind: notify.KindBudgetAlert, Title: "Food is close to its budget", Body: "…", DeepLink: "sprout://home", DedupKey: "alert"}
	goal := notify.Notification{Kind: notify.KindGoalCompleted, Title: "You reached your goal", Body: "…", DeepLink: "sprout://home", DedupKey: "goal"}
	for _, tc := range []struct {
		name   string
		set    string
		pushed []notify.Kind
	}{
		{"all on", "", []notify.Kind{notify.KindBudgetAlert, notify.KindWeeklyRecap, notify.KindGoalCompleted}},
		{"notifications off", "notifications_enabled = false", nil},
		{"budget alerts off", "budget_alerts = false", []notify.Kind{notify.KindWeeklyRecap, notify.KindGoalCompleted}},
		{"weekly recap off", "weekly_recap = false", []notify.Kind{notify.KindBudgetAlert, notify.KindGoalCompleted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			user := testutil.NewTestUser(t, f.pool)
			if tc.set != "" {
				f.exec(t, "UPDATE users SET "+tc.set+" WHERE id = $1", user.ID)
			}
			kinds := map[uuid.UUID]notify.Kind{}
			for _, n := range []notify.Notification{alert, recap("recap"), goal} {
				created, err := f.service.Send(ctx, user.ID, n)
				require.NoError(t, err)
				require.True(t, created)
			}
			for _, n := range f.inbox(t, user) {
				kinds[n.ID] = notify.Kind(n.Kind)
			}
			assert.Len(t, kinds, 3, "the inbox keeps every notification")
			var pushed []notify.Kind
			for _, id := range f.queue.enqueued() {
				pushed = append(pushed, kinds[id])
			}
			assert.Equal(t, tc.pushed, pushed)
		})
	}

	t.Run("turned off while queued", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		user := testutil.NewTestUser(t, f.pool)
		f.register(t, user, token(8), notify.EnvironmentProduction)
		_, err := f.service.Send(ctx, user.ID, alert)
		require.NoError(t, err)
		f.exec(t, "UPDATE users SET budget_alerts = false WHERE id = $1", user.ID)
		d, err := f.service.Deliver(ctx, user.ID, f.queue.enqueued()[0])
		require.NoError(t, err)
		assert.Zero(t, d)
		assert.Empty(t, f.apns.Pushed())
	})
}

func TestSend_Dedup(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := testutil.NewTestUser(t, f.pool)
	other := testutil.NewTestUser(t, f.pool)

	created, err := f.service.Send(ctx, user.ID, recap("recap:2025-W29"))
	require.NoError(t, err)
	assert.True(t, created)
	again := recap("recap:2025-W29")
	again.Body = "Something else."
	created, err = f.service.Send(ctx, user.ID, again)
	require.NoError(t, err)
	assert.False(t, created, "the same event notifies once")
	inbox := f.inbox(t, user)
	require.Len(t, inbox, 1)
	assert.Equal(t, "You spent $120 this week.", inbox[0].Body)
	assert.Len(t, f.queue.enqueued(), 1)

	created, err = f.service.Send(ctx, other.ID, recap("recap:2025-W29"))
	require.NoError(t, err)
	assert.True(t, created, "keys are per user")
}

func TestGoalCompleted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	user := testutil.NewTestUser(t, f.pool)
	service := goals.NewService(f.pool, goals.WithCompletionHook(f.service.GoalCompleted))
	goal := testutil.NewGoal(t, f.pool, user.ID, testutil.GoalTitle("Bike"), testutil.GoalTarget(10000))

	_, _, err := service.Contribute(ctx, user.User, goal.ID, goals.ContributionParams{Kind: goals.KindTopup, AmountMinor: 10000})
	require.NoError(t, err)
	inbox := f.inbox(t, user)
	require.Len(t, inbox, 1)
	assert.Equal(t, "goal_completed", inbox[0].Kind)
	assert.Equal(t, "You reached your goal", inbox[0].Title)
	assert.Equal(t, "Bike is fully saved: $100. Well done!", inbox[0].Body)
	assert.Equal(t, "sprout://goal/"+goal.ID.String(), inbox[0].DeepLink)
	assert.Len(t, f.queue.enqueued(), 1)

	_, _, err = service.Contribute(ctx, user.User, goal.ID, goals.ContributionParams{Kind: goals.KindWithdrawal, AmountMinor: 500})
	require.NoError(t, err)
	_, _, err = service.Contribute(ctx, user.User, goal.ID, goals.ContributionParams{Kind: goals.KindTopup, AmountMinor: 500})
	require.NoError(t, err)
	assert.Len(t, f.inbox(t, user), 1, "completing again does not notify again")
}

func TestImportReady(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	id := uuid.New()
	require.NoError(t, f.service.ImportReady(context.Background(), user.ID, id))
	inbox := f.inbox(t, user)
	require.Len(t, inbox, 1)
	assert.Equal(t, "import_ready", inbox[0].Kind)
	assert.Equal(t, "sprout://import/"+id.String(), inbox[0].DeepLink)
}

func TestListNotifications(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	stranger := testutil.NewTestUser(t, f.pool)
	var ids []uuid.UUID
	for i := range 3 {
		n := testutil.NewNotification(t, f.pool, user.ID)
		// Spread them out, the first the oldest.
		f.exec(t, `UPDATE notifications SET created_at = $2 WHERE id = $1`, n.ID, time.Date(2025, 7, 1+i, 9, 0, 0, 0, time.UTC))
		ids = append(ids, n.ID)
	}
	testutil.NewNotification(t, f.pool, stranger.ID)

	rec := f.do(t, user, http.MethodGet, "/notifications?limit=2", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := testutil.DecodeJSON[httpx.NotificationList](t, rec)
	require.Len(t, page.Items, 2)
	assert.Equal(t, []uuid.UUID{ids[2], ids[1]}, []uuid.UUID{page.Items[0].ID, page.Items[1].ID}, "newest first")
	first := page.Items[0]
	assert.Equal(t, httpx.NotificationKind("weekly_recap"), first.Kind)
	assert.Equal(t, "sprout://stats", *first.DeepLink)
	assert.Nil(t, first.ReadAt)
	require.NotNil(t, page.NextCursor)

	rec = f.do(t, user, http.MethodGet, "/notifications?limit=2&cursor="+*page.NextCursor, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page = testutil.DecodeJSON[httpx.NotificationList](t, rec)
	require.Len(t, page.Items, 1)
	assert.Equal(t, ids[0], page.Items[0].ID)
	assert.Nil(t, page.NextCursor)

	assert.Equal(t, http.StatusBadRequest, f.do(t, user, http.MethodGet, "/notifications?cursor=nope", nil).Code)
	assert.Equal(t, http.StatusBadRequest, f.do(t, user, http.MethodGet, "/notifications?limit=0", nil).Code)

	rec = f.do(t, stranger, http.MethodGet, "/notifications", nil)
	assert.Len(t, testutil.DecodeJSON[httpx.NotificationList](t, rec).Items, 1, "only the caller's inbox")
}

func TestMarkNotificationsRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	stranger := testutil.NewTestUser(t, f.pool)
	a := testutil.NewNotification(t, f.pool, user.ID)
	b := testutil.NewNotification(t, f.pool, user.ID)
	testutil.NewNotification(t, f.pool, user.ID)
	theirs := testutil.NewNotification(t, f.pool, stranger.ID)
	unread := func(u testutil.TestUser) int {
		n := 0
		for _, item := range f.inbox(t, u) {
			if item.ReadAt == nil {
				n++
			}
		}
		return n
	}

	rec := f.do(t, user, http.MethodPost, "/notifications/read", map[string]any{"ids": []uuid.UUID{a.ID, a.ID}})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, 2, unread(user))
	rec = f.do(t, user, http.MethodPost, "/notifications/read", map[string]any{"ids": []uuid.UUID{a.ID}})
	assert.Equal(t, http.StatusNoContent, rec.Code, "already read is fine")

	rec = f.do(t, user, http.MethodPost, "/notifications/read", map[string]any{"ids": []uuid.UUID{b.ID, theirs.ID}})
	assert.Equal(t, http.StatusNotFound, rec.Code, "someone else's notification")
	assert.Equal(t, 2, unread(user), "and nothing is marked")
	assert.Equal(t, 1, unread(stranger))

	for _, body := range []map[string]any{{}, {"ids": []uuid.UUID{}}, {"all": false}, {"all": true, "ids": []uuid.UUID{b.ID}}} {
		rec = f.do(t, user, http.MethodPost, "/notifications/read", body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, fmt.Sprint(body))
	}

	rec = f.do(t, user, http.MethodPost, "/notifications/read", map[string]any{"all": true})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Zero(t, unread(user))
	assert.Equal(t, 1, unread(stranger), "all is the caller's all")
}
