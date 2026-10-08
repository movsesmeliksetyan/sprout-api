package storage_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// put sends body to a presigned upload URL the way the app does: a plain PUT
// with a Content-Type header.
func put(t *testing.T, url, contentType string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestS3_UploadAndDownloadThroughPresignedURLs(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	ctx := context.Background()
	body := []byte("not really a jpeg")
	const key = "u/1/avatar/2"

	_, err := store.Head(ctx, key)
	require.ErrorIs(t, err, storage.ErrNotFound)

	uploadURL, err := store.PresignPut(ctx, key, "image/jpeg", int64(len(body)), time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, put(t, uploadURL, "image/jpeg", body))

	object, err := store.Head(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, storage.Object{Size: int64(len(body)), ContentType: "image/jpeg"}, object)

	downloadURL, err := store.PresignGet(ctx, key, time.Minute)
	require.NoError(t, err)
	resp, err := http.Get(downloadURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, body, got)

	require.NoError(t, store.Delete(ctx, key))
	_, err = store.Head(ctx, key)
	require.ErrorIs(t, err, storage.ErrNotFound)
	assert.NoError(t, store.Delete(ctx, key), "deleting a missing object succeeds")
}

func TestS3_PresignedUploadIsBoundToTypeAndSize(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	ctx := context.Background()
	body := []byte("0123456789")

	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{"another content type", "application/pdf", body},
		{"a larger body", "image/png", append(body, body...)},
		{"a smaller body", "image/png", body[:3]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "u/1/avatar/" + tt.name
			url, err := store.PresignPut(ctx, key, "image/png", int64(len(body)), time.Minute)
			require.NoError(t, err)

			status := put(t, url, tt.contentType, tt.body)

			assert.Equal(t, http.StatusForbidden, status)
			_, err = store.Head(ctx, key)
			assert.ErrorIs(t, err, storage.ErrNotFound, "nothing was stored")
		})
	}
}

func TestS3_PresignedURLsExpire(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	ctx := context.Background()

	url, err := store.PresignPut(ctx, "u/1/avatar/late", "image/png", 3, time.Second)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)

	assert.Equal(t, http.StatusForbidden, put(t, url, "image/png", []byte("abc")))
}

func TestS3_Ready(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)

	assert.NoError(t, store.Ready(context.Background()))
}

func TestS3_ReadyFailsWithoutTheBucket(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Nothing listens here, and construction must not have tried to connect.
	store := storage.NewS3(storage.Config{
		Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "missing",
		AccessKeyID: "key", SecretAccessKey: "secret", UsePathStyle: true,
	})

	assert.Error(t, store.Ready(ctx))
}

func TestS3_TestsDoNotShareABucket(t *testing.T) {
	t.Parallel()
	first, second := testutil.NewStore(t), testutil.NewStore(t)
	ctx := context.Background()

	url, err := first.PresignPut(ctx, "shared-key", "text/csv", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, put(t, url, "text/csv", []byte("x")))

	_, err = second.Head(ctx, "shared-key")
	assert.ErrorIs(t, err, storage.ErrNotFound)
}
