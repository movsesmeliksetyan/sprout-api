package notify_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

type api struct {
	*notify.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

type fixture struct {
	pool    *pgxpool.Pool
	server  http.Handler
	service *notify.Service
	queue   *queue
	apns    *notify.FakeAPNs
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	q, apns := &queue{}, &notify.FakeAPNs{}
	service := notify.NewService(pool, q, notify.WithPusher(apns))
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: notify.NewHandler(service)}),
	).Handler()
	return fixture{pool: pool, server: server, service: service, queue: q, apns: apns}
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body))
}

func (f fixture) devices(t *testing.T, user testutil.TestUser) []db.Device {
	t.Helper()
	devices, err := db.New(f.pool).ListDevices(context.Background(), user.ID)
	require.NoError(t, err)
	return devices
}

func token(b byte) string { return strings.Repeat(string("0123456789abcdef"[b%16]), 64) }

func registration(tok string) map[string]any {
	return map[string]any{"apns_token": tok, "environment": "sandbox", "app_version": "1.0.0", "locale": "en_GB"}
}

func TestRegisterDevice_Upsert(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	device := uuid.New()

	rec := f.do(t, user, http.MethodPut, "/devices/"+device.String(), registration(token(1)))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got := f.devices(t, user)
	require.Len(t, got, 1)
	assert.Equal(t, device, got[0].DeviceID)
	assert.Equal(t, token(1), got[0].ApnsToken)
	assert.Equal(t, "sandbox", got[0].Environment)
	assert.Equal(t, "1.0.0", *got[0].AppVersion)
	assert.Equal(t, "en_GB", *got[0].Locale)
	first := got[0]

	_, err := f.pool.Exec(context.Background(), `UPDATE devices SET invalidated_at = now() WHERE id = $1`, first.ID)
	require.NoError(t, err)
	rec = f.do(t, user, http.MethodPut, "/devices/"+device.String(),
		map[string]any{"apns_token": strings.ToUpper(token(2)), "environment": "production", "app_version": "1.1.0"})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got = f.devices(t, user)
	require.Len(t, got, 1, "the same device is updated")
	assert.Equal(t, first.ID, got[0].ID)
	assert.Equal(t, token(2), got[0].ApnsToken, "a new token, in lower case")
	assert.Equal(t, "production", got[0].Environment)
	assert.Equal(t, "1.1.0", *got[0].AppVersion)
	assert.Nil(t, got[0].Locale)
	assert.Nil(t, got[0].InvalidatedAt, "registering again revives the token")
	assert.True(t, got[0].LastSeenAt.After(first.LastSeenAt) || got[0].LastSeenAt.Equal(first.LastSeenAt))

	second := uuid.New()
	require.Equal(t, http.StatusNoContent, f.do(t, user, http.MethodPut, "/devices/"+second.String(), registration(token(3))).Code)
	assert.Len(t, f.devices(t, user), 2, "a second phone is a second device")
}

func TestRegisterDevice_TokenMovesBetweenUsers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := testutil.NewTestUser(t, f.pool)
	second := testutil.NewTestUser(t, f.pool)
	device := uuid.New()

	require.Equal(t, http.StatusNoContent, f.do(t, first, http.MethodPut, "/devices/"+device.String(), registration(token(4))).Code)
	require.Equal(t, http.StatusNoContent, f.do(t, second, http.MethodPut, "/devices/"+device.String(), registration(token(4))).Code)
	assert.Empty(t, f.devices(t, first), "the token left the first user")
	require.Len(t, f.devices(t, second), 1)

	// The same token from another device id of the same user moves too.
	other := uuid.New()
	require.Equal(t, http.StatusNoContent, f.do(t, second, http.MethodPut, "/devices/"+other.String(), registration(token(4))).Code)
	got := f.devices(t, second)
	require.Len(t, got, 1)
	assert.Equal(t, other, got[0].DeviceID)
}

func TestDeleteDevice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := testutil.NewTestUser(t, f.pool)
	stranger := testutil.NewTestUser(t, f.pool)
	device := uuid.New()
	require.Equal(t, http.StatusNoContent, f.do(t, owner, http.MethodPut, "/devices/"+device.String(), registration(token(5))).Code)

	rec := f.do(t, stranger, http.MethodDelete, "/devices/"+device.String(), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "someone else's device")
	assert.Len(t, f.devices(t, owner), 1)

	assert.Equal(t, http.StatusNoContent, f.do(t, owner, http.MethodDelete, "/devices/"+device.String(), nil).Code)
	assert.Empty(t, f.devices(t, owner))
	assert.Equal(t, http.StatusNotFound, f.do(t, owner, http.MethodDelete, "/devices/"+device.String(), nil).Code, "already gone")
}

func TestRegisterDevice_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for name, tc := range map[string]struct {
		body  map[string]any
		field string
	}{
		"short token":      {map[string]any{"apns_token": "abc123", "environment": "sandbox"}, "apns_token"},
		"not hex":          {map[string]any{"apns_token": strings.Repeat("z", 64), "environment": "sandbox"}, "apns_token"},
		"odd length":       {map[string]any{"apns_token": strings.Repeat("a", 65), "environment": "sandbox"}, "apns_token"},
		"long app version": {map[string]any{"apns_token": token(6), "environment": "sandbox", "app_version": strings.Repeat("1", 33)}, "app_version"},
		"long locale":      {map[string]any{"apns_token": token(6), "environment": "sandbox", "locale": strings.Repeat("e", 36)}, "locale"},
	} {
		rec := f.do(t, user, http.MethodPut, "/devices/"+uuid.NewString(), tc.body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		assert.Contains(t, rec.Body.String(), `"`+tc.field+`"`, name)
	}
	rec := f.do(t, user, http.MethodPut, "/devices/"+uuid.NewString(), map[string]any{"apns_token": token(6), "environment": "staging"})
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, rec.Code, "an unknown environment")
	assert.Empty(t, f.devices(t, user))
}
