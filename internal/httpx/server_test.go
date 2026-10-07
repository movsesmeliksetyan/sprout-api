package httpx

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownRouteReturnsNotFoundEnvelope(t *testing.T) {
	s, _ := newTestServer(t)

	requireEnvelope(t, get(s, "/nope"), http.StatusNotFound, codeNotFound)
}

func TestWrongMethodReturnsEnvelope(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(s, httptest.NewRequest(http.MethodPost, healthzPath, nil))

	requireEnvelope(t, rec, http.StatusMethodNotAllowed, codeBadRequest)
}

// startServer runs s on a loopback port and returns its base URL, the
// function that triggers shutdown, and the channel carrying Run's result.
func startServer(t *testing.T, s *Server) (baseURL string, shutdown context.CancelFunc, done <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- s.Run(ctx, ln) }()

	return "http://" + ln.Addr().String(), cancel, result
}

type clientResult struct {
	status int
	body   string
	err    error
}

func fetch(url string) clientResult {
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()

	resp, err := client.Get(url)
	if err != nil {
		return clientResult{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return clientResult{status: resp.StatusCode, body: string(body), err: err}
}

func TestRun_ServesRequests(t *testing.T) {
	s, _ := newTestServer(t)
	baseURL, shutdown, done := startServer(t, s)

	got := fetch(baseURL + healthzPath)

	require.NoError(t, got.err)
	assert.Equal(t, http.StatusOK, got.status)
	assert.JSONEq(t, `{"status":"ok"}`, got.body)

	shutdown()
	assert.NoError(t, <-done)
}

func TestRun_ShutdownDrainsInFlightRequests(t *testing.T) {
	s, _ := newTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	s.Router().Get("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("finished"))
	})
	baseURL, shutdown, done := startServer(t, s)

	inFlight := make(chan clientResult, 1)
	go func() { inFlight <- fetch(baseURL + "/slow") }()
	<-started

	shutdown()

	// Shutdown has begun once the listener stops accepting connections.
	addr := baseURL[len("http://"):]
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 5*time.Second, 10*time.Millisecond, "new connections must be refused during shutdown")

	select {
	case err := <-done:
		t.Fatalf("Run returned (%v) while a request was still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	got := <-inFlight
	require.NoError(t, got.err, "the in-flight request must complete")
	assert.Equal(t, http.StatusOK, got.status)
	assert.Equal(t, "finished", got.body)
	assert.NoError(t, <-done, "Run returns nil after a clean drain")
}

func TestRun_DrainWindowExceeded(t *testing.T) {
	s, _ := newTestServer(t, WithDrainTimeout(50*time.Millisecond))
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.Router().Get("/stuck", func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})
	baseURL, shutdown, done := startServer(t, s)

	go fetch(baseURL + "/stuck")
	<-started

	shutdown()

	err := <-done
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRun_ServeFailureIsReturned(t *testing.T) {
	s, _ := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())

	err = s.Run(context.Background(), ln)

	assert.ErrorContains(t, err, "serve:")
}

func TestDrainTimeoutDefaultsToRequestTimeout(t *testing.T) {
	s, _ := newTestServer(t, WithRequestTimeout(7*time.Second))

	assert.Equal(t, 7*time.Second, s.drainTimeout)
}
