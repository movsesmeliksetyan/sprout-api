package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

const (
	idempotencyKeyHeader      = "Idempotency-Key"
	idempotencyReplayedHeader = "Idempotency-Replayed"

	idempotencyProcessing = "processing"
	idempotencyCompleted  = "completed"

	// idempotencySaveTimeout bounds the write that records a response. It
	// runs even when the client has gone, so that their retry is a replay.
	idempotencySaveTimeout = 5 * time.Second
)

// IdempotencyConfig tunes the Idempotency middleware. A zero duration means
// its default.
type IdempotencyConfig struct {
	// Routes are the route patterns of the POSTs that honour the header, as
	// api.IdempotentRoutes returns them.
	Routes []string
	// KeyTTL is how long a key replays its response (default 24 h).
	KeyTTL time.Duration
	// Lock is how long a request may take before its key can be claimed by
	// a retry. It must exceed the request timeout (default 1 min).
	Lock time.Duration
	// Wait is how long a duplicate of a request still in flight waits for
	// its response before giving up with a conflict (default 5 s).
	Wait time.Duration
	// Poll is how often a waiting duplicate looks (default 50 ms).
	Poll time.Duration
}

func (c IdempotencyConfig) withDefaults() IdempotencyConfig {
	if c.KeyTTL == 0 {
		c.KeyTTL = 24 * time.Hour
	}
	if c.Lock == 0 {
		c.Lock = time.Minute
	}
	if c.Wait == 0 {
		c.Wait = 5 * time.Second
	}
	if c.Poll == 0 {
		c.Poll = 50 * time.Millisecond
	}
	return c
}

// Idempotency makes the configured POSTs safe to repeat: a request sent again
// with the same Idempotency-Key gets the first one's response and is not run
// a second time. Keys belong to the user, so it goes after the middleware
// that puts the user in the context. Requests without the header, and all
// other routes, pass through untouched.
//
// Only a 2xx response is kept. A request that failed created nothing, so
// its retry runs again instead of replaying the failure.
func Idempotency(database db.DBTX, responder *Responder, logger *slog.Logger, cfg IdempotencyConfig) func(http.Handler) http.Handler {
	cfg = cfg.withDefaults()
	routes := make(map[string]bool, len(cfg.Routes))
	for _, route := range cfg.Routes {
		routes[route] = true
	}
	m := &idempotency{db: db.New(database), responder: responder, logger: logger, cfg: cfg}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get(idempotencyKeyHeader)
			if r.Method != http.MethodPost || header == "" || !routes[chi.RouteContext(r.Context()).RoutePattern()] {
				next.ServeHTTP(w, r)
				return
			}
			m.serve(w, r, next, header)
		})
	}
}

type idempotency struct {
	db        *db.Queries
	responder *Responder
	logger    *slog.Logger
	cfg       IdempotencyConfig
}

func (m *idempotency) serve(w http.ResponseWriter, r *http.Request, next http.Handler, header string) {
	ctx := r.Context()
	user, ok := session.User(ctx)
	if !ok {
		m.responder.Error(w, r, ErrUnauthenticated)
		return
	}
	key, err := uuid.Parse(header)
	if err != nil {
		m.responder.Error(w, r, WithMessage(ErrBadRequest, "Idempotency-Key must be a UUID."))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.responder.RequestError(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := requestHash(r, body)

	deadline := time.Now().Add(m.cfg.Wait)
	for {
		now := time.Now()
		owned, err := m.db.AcquireIdempotencyKey(ctx, db.AcquireIdempotencyKeyParams{
			UserID:      user.ID,
			Key:         key,
			RequestHash: hash,
			LockedUntil: now.Add(m.cfg.Lock),
			ExpiresAt:   now.Add(m.cfg.KeyTTL),
		})
		if err == nil {
			m.run(w, r, next, owned)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			m.responder.Error(w, r, fmt.Errorf("idempotency: acquire: %w", err))
			return
		}

		// Someone else holds the key.
		existing, err := m.db.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{UserID: user.ID, Key: key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Released since the attempt above: that request failed, so this
			// one runs. Try to acquire again.
			continue
		case err != nil:
			m.responder.Error(w, r, fmt.Errorf("idempotency: get: %w", err))
			return
		case !bytes.Equal(existing.RequestHash, hash):
			m.responder.Error(w, r, WithMessage(ErrConflict,
				"This Idempotency-Key was already used for a different request."))
			return
		case existing.Status == idempotencyCompleted:
			replay(w, existing)
			return
		}

		// The same request is still in flight. Wait for its response.
		if time.Now().After(deadline) {
			w.Header().Set("Retry-After", strconv.Itoa(1))
			m.responder.Error(w, r, WithMessage(ErrConflict,
				"A request with this Idempotency-Key is still being processed."))
			return
		}
		select {
		case <-ctx.Done():
			m.responder.Error(w, r, ctx.Err())
			return
		case <-time.After(m.cfg.Poll):
		}
	}
}

// run executes the request that owns the key, then records its response or
// gives the key up.
func (m *idempotency) run(w http.ResponseWriter, r *http.Request, next http.Handler, owned db.IdempotencyKey) {
	rec := &responseRecorder{ResponseWriter: w}
	finished := false
	defer func() {
		// The request's context may already be cancelled; the record must be
		// written regardless.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), idempotencySaveTimeout)
		defer cancel()

		var err error
		if finished && rec.succeeded() {
			status := int32(rec.status)
			contentType := rec.Header().Get("Content-Type")
			_, err = m.db.CompleteIdempotencyKey(ctx, db.CompleteIdempotencyKeyParams{
				UserID:              owned.UserID,
				Key:                 owned.Key,
				LockedUntil:         owned.LockedUntil,
				ResponseStatus:      &status,
				ResponseContentType: &contentType,
				ResponseBody:        rec.body.Bytes(),
			})
		} else {
			_, err = m.db.ReleaseIdempotencyKey(ctx, db.ReleaseIdempotencyKeyParams{
				UserID:      owned.UserID,
				Key:         owned.Key,
				LockedUntil: owned.LockedUntil,
			})
		}
		if err != nil {
			// The response has gone out. The key frees itself when its lock
			// runs out.
			m.logger.ErrorContext(ctx, "idempotency key not updated", slog.String("error", err.Error()))
		}
	}()

	next.ServeHTTP(rec, r)
	finished = true
}

// replay writes a stored response.
func replay(w http.ResponseWriter, stored db.IdempotencyKey) {
	if stored.ResponseContentType != nil && *stored.ResponseContentType != "" {
		w.Header().Set("Content-Type", *stored.ResponseContentType)
	}
	w.Header().Set(idempotencyReplayedHeader, "true")
	status := http.StatusOK
	if stored.ResponseStatus != nil {
		status = int(*stored.ResponseStatus)
	}
	w.WriteHeader(status)
	_, _ = w.Write(stored.ResponseBody)
}

// requestHash identifies what a key was used for: the operation, the
// resource and the exact body.
func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// responseRecorder keeps a copy of what a handler writes.
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// succeeded reports whether the handler itself answered 2xx. A handler that
// wrote nothing did not answer.
func (r *responseRecorder) succeeded() bool {
	return r.status >= 200 && r.status < 300
}

// PurgeIdempotencyKeys deletes the keys that have expired and reports how
// many there were. Expired keys are never replayed, so this only reclaims
// space.
func PurgeIdempotencyKeys(ctx context.Context, database db.DBTX) (int64, error) {
	n, err := db.New(database).DeleteExpiredIdempotencyKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("idempotency: purge: %w", err)
	}
	return n, nil
}

// PurgeIdempotencyKeysEvery purges once and then at every interval, until
// ctx is cancelled. A failed purge is logged and tried again next time.
func PurgeIdempotencyKeysEvery(ctx context.Context, database db.DBTX, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		switch removed, err := PurgeIdempotencyKeys(ctx, database); {
		case ctx.Err() != nil:
			return
		case err != nil:
			logger.WarnContext(ctx, "idempotency keys not purged", slog.String("error", err.Error()))
		case removed > 0:
			logger.InfoContext(ctx, "idempotency keys purged", slog.Int64("removed", removed))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
