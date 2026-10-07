package testutil

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

// Logger returns a logger that writes to the test's log, so its output is
// shown only when the test fails or runs with -v. It must not be used after
// the test has finished.
func Logger(t testing.TB) *slog.Logger {
	return logging.New(testWriter{t}, slog.LevelDebug)
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// JSONRequest builds a request whose body is body encoded as JSON. A nil
// body sends none.
func JSONRequest(t testing.TB, method, target string, body any) *http.Request {
	t.Helper()
	if body == nil {
		return httptest.NewRequest(method, target, nil)
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// Do runs req against handler and returns what it wrote.
func Do(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// DecodeJSON decodes the response body into a T.
func DecodeJSON[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), "body: %s", rec.Body.String())
	return v
}
