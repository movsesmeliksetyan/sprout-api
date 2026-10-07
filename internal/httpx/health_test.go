package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t, WithReadinessCheck("postgres", func(context.Context) error {
		return errors.New("down")
	}))

	rec := get(s, healthzPath)

	assert.Equal(t, http.StatusOK, rec.Code, "liveness does not depend on readiness checks")
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get(requestIDHeader))
}

func TestReadyz(t *testing.T) {
	pass := func(context.Context) error { return nil }

	t.Run("ready with no checks", func(t *testing.T) {
		s, _ := newTestServer(t)

		rec := get(s, readyzPath)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	})

	t.Run("ready when every check passes", func(t *testing.T) {
		s, _ := newTestServer(t, WithReadinessCheck("postgres", pass), WithReadinessCheck("storage", pass))

		assert.Equal(t, http.StatusOK, get(s, readyzPath).Code)
	})

	t.Run("one failing check makes it unavailable", func(t *testing.T) {
		s, logs := newTestServer(t,
			WithReadinessCheck("postgres", pass),
			WithReadinessCheck("storage", func(context.Context) error {
				return errors.New("dial tcp 10.0.0.7:9000: connection refused")
			}),
		)

		rec := get(s, readyzPath)

		requireEnvelope(t, rec, http.StatusServiceUnavailable, codeUnavailable)
		assert.NotContains(t, rec.Body.String(), "10.0.0.7", "check errors are logged, not returned")

		line := logs.record(t, "readiness check failed")
		assert.Equal(t, "storage", line["check"])
		assert.Contains(t, line["error"], "connection refused")
	})

	t.Run("a hanging check is cut off", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		s, logs := newTestServer(t, WithReadinessCheck("postgres", func(context.Context) error {
			<-release // ignores its context on purpose
			return nil
		}))
		s.checkTimeout = 20 * time.Millisecond

		start := time.Now()
		rec := get(s, readyzPath)

		requireEnvelope(t, rec, http.StatusServiceUnavailable, codeUnavailable)
		assert.Less(t, time.Since(start), time.Second)
		assert.Contains(t, logs.record(t, "readiness check failed")["error"], "deadline exceeded")
	})
}
