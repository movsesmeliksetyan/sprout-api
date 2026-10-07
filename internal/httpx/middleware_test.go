package httpx

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		wantKept bool
	}{
		{name: "generated when absent", incoming: ""},
		{name: "well-formed id is kept", incoming: "client-abc_123.4", wantKept: true},
		{name: "id with unsafe characters is replaced", incoming: "abc\"def"},
		{name: "id with spaces is replaced", incoming: "abc def"},
		{name: "oversized id is replaced", incoming: strings.Repeat("a", maxRequestIDLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			var seen string
			s.Router().Get("/id", func(_ http.ResponseWriter, r *http.Request) { seen = RequestID(r.Context()) })
			req := httptest.NewRequest(http.MethodGet, "/id", nil)
			if tt.incoming != "" {
				req.Header.Set(requestIDHeader, tt.incoming)
			}

			rec := do(s, req)

			got := rec.Header().Get(requestIDHeader)
			assert.Equal(t, got, seen, "handler sees the id that is returned")
			if tt.wantKept {
				assert.Equal(t, tt.incoming, got)
				return
			}
			assert.NotEqual(t, tt.incoming, got)
			parsed, err := uuid.Parse(got)
			require.NoError(t, err)
			assert.Equal(t, uuid.Version(7), parsed.Version())
		})
	}
}

func TestRequestID_OnHandlerLogLines(t *testing.T) {
	s, logs := newTestServer(t)
	s.Router().Get("/work", func(_ http.ResponseWriter, r *http.Request) {
		s.logger.InfoContext(r.Context(), "doing work")
	})

	rec := get(s, "/work")

	assert.Equal(t, rec.Header().Get(requestIDHeader), logs.record(t, "doing work")["request_id"])
}

func TestAccessLog(t *testing.T) {
	s, logs := newTestServer(t)
	s.Router().Post("/things", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("made"))
	})

	rec := do(s, httptest.NewRequest(http.MethodPost, "/things?q=private-search-term", nil))

	line := logs.record(t, "request")
	assert.Equal(t, "INFO", line["level"])
	assert.Equal(t, "POST", line["method"])
	assert.Equal(t, "/things", line["path"])
	assert.EqualValues(t, http.StatusCreated, line["status"])
	assert.EqualValues(t, 4, line["bytes"])
	assert.Contains(t, line, "duration_ms")
	assert.Contains(t, line, "remote_addr")
	assert.Equal(t, rec.Header().Get(requestIDHeader), line["request_id"])
	assert.NotContains(t, logs.String(), "private-search-term", "query strings must not be logged")
}

func TestAccessLog_ImplicitStatusIsOK(t *testing.T) {
	s, logs := newTestServer(t)
	s.Router().Get("/silent", func(http.ResponseWriter, *http.Request) {})

	get(s, "/silent")

	assert.EqualValues(t, http.StatusOK, logs.record(t, "request")["status"])
}

func TestAccessLog_HealthChecksAtDebug(t *testing.T) {
	s, logs := newTestServer(t)

	get(s, healthzPath)

	assert.Equal(t, slog.LevelDebug.String(), logs.record(t, "request")["level"])
}

func TestRecoverer_PanicReturnsInternalEnvelope(t *testing.T) {
	s, logs := newTestServer(t)
	s.Router().Get("/panic", func(http.ResponseWriter, *http.Request) { panic("kaboom: secret detail") })

	rec := get(s, "/panic")

	body := requireEnvelope(t, rec, http.StatusInternalServerError, codeInternal)
	assert.NotContains(t, rec.Body.String(), "kaboom", "the panic value must not reach the client")

	line := logs.record(t, "panic recovered")
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, "kaboom: secret detail", line["panic"])
	assert.Contains(t, line["stack"], "middleware_test.go")
	assert.Equal(t, body.RequestID, line["request_id"])
	assert.EqualValues(t, http.StatusInternalServerError, logs.record(t, "request")["status"])
}

func TestRecoverer_PanicAfterResponseStarted(t *testing.T) {
	s, logs := newTestServer(t)
	s.Router().Get("/late-panic", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("too late")
	})

	rec := get(s, "/late-panic")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "partial", rec.Body.String(), "no second body is appended")
	logs.record(t, "panic recovered")
}

func TestRecoverer_AbortHandlerIsNotSwallowed(t *testing.T) {
	s, _ := newTestServer(t)
	s.Router().Get("/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { get(s, "/abort") })
}

func TestTimeout(t *testing.T) {
	s, _ := newTestServer(t, WithRequestTimeout(20*time.Millisecond))
	s.Router().Get("/slow", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	s.Router().Get("/fast", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	t.Run("handler past the deadline gets unavailable", func(t *testing.T) {
		requireEnvelope(t, get(s, "/slow"), http.StatusServiceUnavailable, codeUnavailable)
	})

	t.Run("fast handler is unaffected", func(t *testing.T) {
		rec := get(s, "/fast")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "ok", rec.Body.String())
	})
}

func TestBodyLimit(t *testing.T) {
	const limit = 16
	s, _ := newTestServer(t, WithMaxBodyBytes(limit))
	var readErr error
	s.Router().Post("/echo", func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		body, readErr = io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})

	t.Run("declared length over the limit is rejected up front", func(t *testing.T) {
		readErr = nil
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("x", limit+1)))

		requireEnvelope(t, do(s, req), http.StatusRequestEntityTooLarge, codePayloadTooLarge)
		assert.NoError(t, readErr, "the handler must not run")
	})

	t.Run("undeclared oversize body fails on read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("x", limit+1)))
		req.ContentLength = -1

		do(s, req)

		var tooLarge *http.MaxBytesError
		require.ErrorAs(t, readErr, &tooLarge)
		assert.EqualValues(t, limit, tooLarge.Limit)
	})

	t.Run("body at the limit passes", func(t *testing.T) {
		payload := strings.Repeat("x", limit)

		rec := do(s, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(payload)))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, payload, rec.Body.String())
	})
}

func TestGzip(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("compressed when the client accepts gzip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, healthzPath, nil)
		req.Header.Set("Accept-Encoding", "gzip")

		rec := do(s, req)

		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
		zr, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.JSONEq(t, `{"status":"ok"}`, string(body))
	})

	t.Run("plain otherwise", func(t *testing.T) {
		rec := get(s, healthzPath)

		assert.Empty(t, rec.Header().Get("Content-Encoding"))
		assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	})

	t.Run("error envelopes are compressed too", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		req.Header.Set("Accept-Encoding", "gzip")

		rec := do(s, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	})
}
