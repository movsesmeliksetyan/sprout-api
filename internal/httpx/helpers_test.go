package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

// syncBuffer is a bytes.Buffer safe to write from server goroutines while a
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records decodes every log line written so far.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "log line is not JSON: %s", line)
		out = append(out, rec)
	}
	return out
}

// record returns the single log line with the given message.
func (b *syncBuffer) record(t *testing.T, msg string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, rec := range b.records(t) {
		if rec["msg"] == msg {
			found = append(found, rec)
		}
	}
	require.Len(t, found, 1, "want exactly one %q log line in:\n%s", msg, b.String())
	return found[0]
}

func newTestServer(t *testing.T, opts ...Option) (*Server, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	return NewServer(logging.New(logs, slog.LevelDebug), opts...), logs
}

func do(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func get(s *Server, target string) *httptest.ResponseRecorder {
	return do(s, httptest.NewRequest(http.MethodGet, target, nil))
}

// requireEnvelope asserts the response is the error envelope with the given
// status and code, and returns its body.
func requireEnvelope(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var raw map[string]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.Contains(t, raw["error"], "fields", "optional fields are present as null, not omitted")

	var env errorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, code, env.Error.Code)
	require.NotEmpty(t, env.Error.Message)
	require.NotEmpty(t, env.Error.RequestID)
	require.Equal(t, rec.Header().Get(requestIDHeader), env.Error.RequestID, "envelope and header carry the same id")
	return env.Error
}
