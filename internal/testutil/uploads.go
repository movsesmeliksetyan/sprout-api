package testutil

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

// Upload registers an upload for the user and sends content to it, the
// way the app does; it returns the upload's id, ready to be consumed.
func Upload(t testing.TB, service *uploads.Service, userID uuid.UUID, purpose uploads.Purpose, contentType, filename string, content []byte) uuid.UUID {
	t.Helper()
	params := uploads.CreateParams{Purpose: purpose, ContentType: contentType, SizeBytes: int64(len(content))}
	if filename != "" {
		params.Filename = &filename
	}
	created, err := service.Create(context.Background(), userID, params)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPut, created.URL, bytes.NewReader(content))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return created.Upload.ID
}
