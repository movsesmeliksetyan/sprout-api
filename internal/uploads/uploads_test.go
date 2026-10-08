package uploads_test

import (
	"bytes"
	"context"
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

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// api is the server's API with only the uploads handler implemented.
type api struct {
	*uploads.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

// fixture is a server with real Postgres and object storage behind it.
type fixture struct {
	pool    *pgxpool.Pool
	store   *storage.S3
	service *uploads.Service
	server  http.Handler
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool, store := testutil.NewDB(t), testutil.NewStore(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	service := uploads.NewService(pool, store)
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: uploads.NewHandler(service)}),
	).Handler()
	return fixture{pool: pool, store: store, service: service, server: server}
}

func (f fixture) post(t *testing.T, user testutil.TestUser, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1/uploads", body))
}

// create asks for an upload slot and returns the response.
func (f fixture) create(t *testing.T, user testutil.TestUser, purpose, contentType string, size int) httpx.Upload {
	t.Helper()
	rec := f.post(t, user, map[string]any{"purpose": purpose, "content_type": contentType, "size_bytes": size})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Upload](t, rec)
}

// send PUTs body to an upload URL with the headers the API said to send.
func send(t *testing.T, upload httpx.Upload, body []byte) int {
	t.Helper()
	return sendTo(t, upload.UploadURL, upload.Headers, body)
}

func sendTo(t *testing.T, url string, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (f fixture) stored(t *testing.T, id uuid.UUID) db.Upload {
	t.Helper()
	var u db.Upload
	err := f.pool.QueryRow(context.Background(),
		"SELECT id, user_id, purpose, object_key, content_type, size_bytes, filename, status, expires_at FROM uploads WHERE id = $1", id).
		Scan(&u.ID, &u.UserID, &u.Purpose, &u.ObjectKey, &u.ContentType, &u.SizeBytes, &u.Filename, &u.Status, &u.ExpiresAt)
	require.NoError(t, err)
	return u
}

func (f fixture) consume(user testutil.TestUser, id uuid.UUID, purpose uploads.Purpose) (db.Upload, error) {
	return f.service.Consume(context.Background(), db.New(f.pool), user.ID, id, purpose)
}

type errorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

var jpeg = bytes.Repeat([]byte("jpeg"), 256)

func TestUploadThroughThePresignedURLAndConsume(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	rec := f.post(t, user, map[string]any{
		"purpose": "receipt", "content_type": "image/jpeg", "size_bytes": len(jpeg), "filename": " IMG_0042.jpg ",
	})

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	upload := testutil.DecodeJSON[httpx.Upload](t, rec)
	assert.Equal(t, uuid.Version(7), upload.ID.Version())
	assert.Equal(t, httpx.UploadMethodPUT, upload.Method)
	assert.Equal(t, map[string]string{"Content-Type": "image/jpeg"}, upload.Headers)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), upload.ExpiresAt, time.Minute)
	assert.Equal(t, time.UTC, upload.ExpiresAt.Location())

	row := f.stored(t, upload.ID)
	assert.Equal(t, user.ID, row.UserID)
	assert.Equal(t, "receipt", row.Purpose)
	assert.Equal(t, "u/"+user.ID.String()+"/receipt/"+upload.ID.String(), row.ObjectKey)
	assert.Equal(t, "pending", row.Status)
	assert.Equal(t, "IMG_0042.jpg", *row.Filename)
	assert.Contains(t, upload.UploadURL, row.ObjectKey)

	_, err := f.consume(user, upload.ID, uploads.PurposeReceipt)
	require.ErrorIs(t, err, uploads.ErrNotUploaded, "nothing has been sent yet")

	require.Equal(t, http.StatusOK, send(t, upload, jpeg))

	consumed, err := f.consume(user, upload.ID, uploads.PurposeReceipt)
	require.NoError(t, err)
	assert.Equal(t, "consumed", consumed.Status)
	assert.Equal(t, int64(len(jpeg)), consumed.SizeBytes)
	assert.Equal(t, row.ObjectKey, consumed.ObjectKey)
	assert.Equal(t, "consumed", f.stored(t, upload.ID).Status)

	_, err = f.consume(user, upload.ID, uploads.PurposeReceipt)
	assert.ErrorIs(t, err, uploads.ErrAlreadyConsumed, "an upload is used once")
}

func TestConsume_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner, stranger := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)

	uploaded := func(t *testing.T, purpose, contentType string) httpx.Upload {
		t.Helper()
		upload := f.create(t, owner, purpose, contentType, len(jpeg))
		require.Equal(t, http.StatusOK, send(t, upload, jpeg))
		return upload
	}

	t.Run("wrong purpose", func(t *testing.T) {
		upload := uploaded(t, "receipt", "image/jpeg")

		_, err := f.consume(owner, upload.ID, uploads.PurposeAvatar)

		require.ErrorIs(t, err, uploads.ErrWrongPurpose)
		assert.Equal(t, "pending", f.stored(t, upload.ID).Status)
	})

	t.Run("another user's upload looks like no upload", func(t *testing.T) {
		upload := uploaded(t, "avatar", "image/jpeg")

		_, err := f.consume(stranger, upload.ID, uploads.PurposeAvatar)

		require.ErrorIs(t, err, uploads.ErrNotFound)
		assert.Equal(t, "pending", f.stored(t, upload.ID).Status)
	})

	t.Run("an id that was never issued", func(t *testing.T) {
		_, err := f.consume(owner, uuid.New(), uploads.PurposeAvatar)

		assert.ErrorIs(t, err, uploads.ErrNotFound)
	})

	t.Run("never uploaded", func(t *testing.T) {
		upload := f.create(t, owner, "avatar", "image/png", 1024)

		_, err := f.consume(owner, upload.ID, uploads.PurposeAvatar)

		require.ErrorIs(t, err, uploads.ErrNotUploaded)
		assert.Equal(t, "pending", f.stored(t, upload.ID).Status)
	})

	t.Run("the upload URL refuses a file larger than declared", func(t *testing.T) {
		upload := f.create(t, owner, "avatar", "image/jpeg", 16)

		status := send(t, upload, jpeg)

		assert.Equal(t, http.StatusForbidden, status)
		_, err := f.consume(owner, upload.ID, uploads.PurposeAvatar)
		assert.ErrorIs(t, err, uploads.ErrNotUploaded)
	})

	t.Run("an oversize object is refused and removed", func(t *testing.T) {
		upload := f.create(t, owner, "avatar", "image/jpeg", 1024)
		key := f.stored(t, upload.ID).ObjectKey
		// Put a file over the avatar limit at the upload's key by another route.
		tooBig := make([]byte, 5<<20+1)
		url, err := f.store.PresignPut(context.Background(), key, "image/jpeg", int64(len(tooBig)), time.Minute)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, sendTo(t, url, map[string]string{"Content-Type": "image/jpeg"}, tooBig))

		_, err = f.consume(owner, upload.ID, uploads.PurposeAvatar)

		require.ErrorIs(t, err, uploads.ErrTooLarge)
		_, err = f.store.Head(context.Background(), key)
		assert.ErrorIs(t, err, storage.ErrNotFound, "the object is deleted")
		assert.Equal(t, "pending", f.stored(t, upload.ID).Status)
	})

	t.Run("a consume in a transaction that rolls back leaves the upload usable", func(t *testing.T) {
		upload := uploaded(t, "statement", "text/csv")
		ctx := context.Background()
		tx, err := f.pool.Begin(ctx)
		require.NoError(t, err)

		_, err = f.service.Consume(ctx, db.New(tx), owner.ID, upload.ID, uploads.PurposeStatement)
		require.NoError(t, err)
		require.NoError(t, tx.Rollback(ctx))

		_, err = f.consume(owner, upload.ID, uploads.PurposeStatement)
		assert.NoError(t, err)
	})
}

func TestRejection(t *testing.T) {
	for _, err := range []error{
		uploads.ErrNotFound, uploads.ErrWrongPurpose, uploads.ErrNotUploaded, uploads.ErrTooLarge, uploads.ErrAlreadyConsumed,
	} {
		reason, ok := uploads.Rejection(err)
		assert.True(t, ok, err)
		assert.NotEmpty(t, reason, err)
	}
	_, ok := uploads.Rejection(context.DeadlineExceeded)
	assert.False(t, ok)
	_, ok = uploads.Rejection(nil)
	assert.False(t, ok)
}

func TestCreateUpload_AcceptedTypesAndSizes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	const mb = 1 << 20

	accepted := map[string]struct {
		types []string
		max   int
	}{
		"avatar":  {[]string{"image/jpeg", "image/png", "image/heic"}, 5 * mb},
		"receipt": {[]string{"image/jpeg", "image/png", "image/heic"}, 10 * mb},
		"statement": {[]string{"text/csv", "application/x-ofx",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/pdf", "application/octet-stream"}, 15 * mb},
	}

	for purpose, rule := range accepted {
		for _, contentType := range rule.types {
			t.Run(purpose+" "+contentType, func(t *testing.T) {
				upload := f.create(t, user, purpose, contentType, rule.max)

				assert.Equal(t, contentType, upload.Headers["Content-Type"])
			})
		}
		t.Run(purpose+" one byte over the limit", func(t *testing.T) {
			rec := f.post(t, user, map[string]any{"purpose": purpose, "content_type": rule.types[0], "size_bytes": rule.max + 1})

			require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body: %s", rec.Body.String())
			body := testutil.DecodeJSON[errorBody](t, rec)
			assert.Equal(t, "payload_too_large", body.Error.Code)
			assert.Contains(t, body.Error.Message, "MB")
		})
	}

	t.Run("the content type is matched without regard to case", func(t *testing.T) {
		upload := f.create(t, user, "avatar", "Image/JPEG", 10)

		assert.Equal(t, "image/jpeg", upload.Headers["Content-Type"])
	})
}

func TestCreateUpload_InvalidRequests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	tests := []struct {
		name       string
		body       map[string]any
		wantFields []string
	}{
		{"unknown purpose", map[string]any{"purpose": "video", "content_type": "image/png", "size_bytes": 10}, []string{"purpose"}},
		{"a statement type for an avatar", map[string]any{"purpose": "avatar", "content_type": "application/pdf", "size_bytes": 10}, []string{"content_type"}},
		{"an image for a statement", map[string]any{"purpose": "statement", "content_type": "image/png", "size_bytes": 10}, []string{"content_type"}},
		{"a type nobody accepts", map[string]any{"purpose": "receipt", "content_type": "image/gif", "size_bytes": 10}, []string{"content_type"}},
		{"a type with parameters", map[string]any{"purpose": "statement", "content_type": "text/csv; charset=utf-8", "size_bytes": 10}, []string{"content_type"}},
		{"zero size", map[string]any{"purpose": "avatar", "content_type": "image/png", "size_bytes": 0}, []string{"size_bytes"}},
		{"negative size", map[string]any{"purpose": "avatar", "content_type": "image/png", "size_bytes": -5}, []string{"size_bytes"}},
		{"filename too long", map[string]any{"purpose": "avatar", "content_type": "image/png", "size_bytes": 10, "filename": strings.Repeat("a", 256)}, []string{"filename"}},
		{"several problems at once", map[string]any{"purpose": "avatar", "content_type": "text/csv", "size_bytes": 0}, []string{"content_type", "size_bytes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.post(t, user, tt.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
			body := testutil.DecodeJSON[errorBody](t, rec)
			fields := make([]string, 0, len(body.Error.Fields))
			for field := range body.Error.Fields {
				fields = append(fields, field)
			}
			assert.ElementsMatch(t, tt.wantFields, fields)
		})
	}

	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM uploads").Scan(&count))
	assert.Zero(t, count, "a refused request leaves no row")

	t.Run("a blank filename is no filename", func(t *testing.T) {
		rec := f.post(t, user, map[string]any{"purpose": "avatar", "content_type": "image/png", "size_bytes": 10, "filename": "  "})

		require.Equal(t, http.StatusCreated, rec.Code)
		assert.Nil(t, f.stored(t, testutil.DecodeJSON[httpx.Upload](t, rec).ID).Filename)
	})

	t.Run("missing fields", func(t *testing.T) {
		rec := f.post(t, user, map[string]any{})

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("no token", func(t *testing.T) {
		rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodPost, "/v1/uploads",
			map[string]any{"purpose": "avatar", "content_type": "image/png", "size_bytes": 10}))

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
