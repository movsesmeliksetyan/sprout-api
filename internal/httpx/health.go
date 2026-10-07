package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
)

const (
	healthzPath = "/healthz"
	readyzPath  = "/readyz"
)

// Check reports whether a dependency is reachable. It must honour ctx.
type Check func(ctx context.Context) error

type namedCheck struct {
	name  string
	check Check
}

type statusBody struct {
	Status string `json:"status"`
}

// handleHealthz answers as long as the process is serving requests.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, statusBody{Status: "ok"})
}

// handleReadyz runs every readiness check. Failures are logged by name; the
// response says only that the service is not ready.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.checkTimeout)
	defer cancel()

	errs := make([]error, len(s.checks))
	var wg sync.WaitGroup
	for i, c := range s.checks {
		wg.Go(func() { errs[i] = runCheck(ctx, c.check) })
	}
	wg.Wait()

	ready := true
	for i, err := range errs {
		if err != nil {
			ready = false
			s.logger.WarnContext(r.Context(), "readiness check failed",
				slog.String("check", s.checks[i].name),
				slog.String("error", err.Error()),
			)
		}
	}
	if !ready {
		// Each failure is logged above by name, so write without logging again.
		writeResolution(w, r, resolve(WithMessage(ErrUnavailable, "The service is not ready."), errInternal))
		return
	}
	WriteJSON(w, http.StatusOK, statusBody{Status: "ok"})
}

// runCheck stops waiting when ctx ends, even if the check itself does not.
func runCheck(ctx context.Context, check Check) error {
	done := make(chan error, 1)
	go func() { done <- check(ctx) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
