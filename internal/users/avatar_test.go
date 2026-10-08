package users_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// avatarFixture is a users server whose avatars live in real object storage.
type avatarFixture struct {
	pool    *pgxpool.Pool
	store   *storage.S3
	uploads *uploads.Service
	server  http.Handler
}

func newAvatarFixture(t *testing.T) avatarFixture {
	t.Helper()
	pool, store := testutil.NewDB(t), testutil.NewStore(t)
	uploadService := uploads.NewService(pool, store)
	server := newServer(t, pool, users.WithAvatars(uploadService, store, testutil.Logger(t)))
	return avatarFixture{pool: pool, store: store, uploads: uploadService, server: server}
}

// upload registers an upload for the user and sends content to it.
func (f avatarFixture) upload(t *testing.T, user testutil.TestUser, purpose uploads.Purpose, content []byte) uuid.UUID {
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

func (f avatarFixture) me(t *testing.T, user testutil.TestUser) httpx.Me {
	t.Helper()
	rec := testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, http.MethodGet, "/v1/me", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.Me](t, rec)
}

func (f avatarFixture) objectExists(t *testing.T, key string) bool {
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

func TestAvatar_SetReplaceAndRemove(t *testing.T) {
	t.Parallel()
	f := newAvatarFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	first, second := []byte("the first picture"), []byte("the second, larger picture")

	require.Nil(t, f.me(t, user).AvatarURL, "a new user has no avatar")

	// Set.
	firstID := f.upload(t, user, uploads.PurposeAvatar, first)
	rec := patchMe(t, f.server, user, map[string]any{"avatar_upload_id": firstID})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	patched := testutil.DecodeJSON[httpx.Me](t, rec)
	require.NotNil(t, patched.AvatarURL, "the PATCH response already has the link")
	status, body := download(t, *patched.AvatarURL)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, first, body)

	firstKey := *storedUser(t, f.pool, user).AvatarKey
	assert.Equal(t, "u/"+user.ID.String()+"/avatar/"+firstID.String(), firstKey)

	fromGet := f.me(t, user).AvatarURL
	require.NotNil(t, fromGet)
	_, body = download(t, *fromGet)
	assert.Equal(t, first, body)

	// An unrelated change keeps it.
	rec = patchMe(t, f.server, user, map[string]any{"name": "Daisy Walker"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, firstKey, *storedUser(t, f.pool, user).AvatarKey)
	assert.True(t, f.objectExists(t, firstKey))

	// Replace.
	secondID := f.upload(t, user, uploads.PurposeAvatar, second)
	rec = patchMe(t, f.server, user, map[string]any{"avatar_upload_id": secondID})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	secondKey := *storedUser(t, f.pool, user).AvatarKey
	assert.NotEqual(t, firstKey, secondKey)
	_, body = download(t, *f.me(t, user).AvatarURL)
	assert.Equal(t, second, body)
	assert.False(t, f.objectExists(t, firstKey), "the replaced picture is deleted")

	// The same upload cannot be used twice.
	rec = patchMe(t, f.server, user, map[string]any{"avatar_upload_id": secondID})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.True(t, f.objectExists(t, secondKey), "a refused change deletes nothing")

	// Remove.
	rec = patchMe(t, f.server, user, map[string]any{"avatar_upload_id": nil})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Nil(t, testutil.DecodeJSON[httpx.Me](t, rec).AvatarURL)
	assert.Nil(t, storedUser(t, f.pool, user).AvatarKey)
	assert.False(t, f.objectExists(t, secondKey), "the removed picture is deleted")
}

func TestAvatar_RefusedUploads(t *testing.T) {
	t.Parallel()
	f := newAvatarFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	picture := []byte("a picture")

	neverSent, err := f.uploads.Create(context.Background(), user.ID, uploads.CreateParams{
		Purpose: uploads.PurposeAvatar, ContentType: "image/png", SizeBytes: 10,
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		uploadID uuid.UUID
	}{
		{"an upload made for a receipt", f.upload(t, user, uploads.PurposeReceipt, picture)},
		{"another user's upload", f.upload(t, other, uploads.PurposeAvatar, picture)},
		{"an upload whose file never arrived", neverSent.Upload.ID},
		{"an id that was never issued", uuid.New()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := patchMe(t, f.server, user, map[string]any{"avatar_upload_id": tt.uploadID, "name": "Should Not Stick"})

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
			body := testutil.DecodeJSON[errorBody](t, rec)
			assert.Equal(t, "validation_failed", body.Error.Code)
			assert.Len(t, body.Error.Fields, 1)
			assert.NotEmpty(t, body.Error.Fields["avatar_upload_id"])
			assert.Equal(t, user.User, storedUser(t, f.pool, user), "nothing is written, not even the name")
		})
	}

	t.Run("the other user's upload is still theirs to use", func(t *testing.T) {
		rec := patchMe(t, f.server, other, map[string]any{"avatar_upload_id": tests[1].uploadID})

		assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}
