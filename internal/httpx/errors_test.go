package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

// respond runs write against a request that has passed through the
// request-id middleware, as every real request has.
func respond(t *testing.T, write func(rs *Responder, w http.ResponseWriter, r *http.Request)) (*httptest.ResponseRecorder, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	rs := NewResponder(logging.New(logs, slog.LevelDebug))
	rec := httptest.NewRecorder()
	handler := requestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write(rs, w, r) }))

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	return rec, logs
}

func respondError(t *testing.T, err error) (*httptest.ResponseRecorder, *syncBuffer) {
	t.Helper()
	return respond(t, func(rs *Responder, w http.ResponseWriter, r *http.Request) { rs.Error(w, r, err) })
}

func TestResponder_MapsEveryErrorType(t *testing.T) {
	unknown := errors.New("pq: relation \"users\" does not exist")

	tests := []struct {
		name       string
		err        error
		request    bool // use RequestError instead of Error
		wantStatus int
		wantCode   string
	}{
		{name: "bad request", err: ErrBadRequest, wantStatus: 400, wantCode: "bad_request"},
		{name: "unauthenticated", err: ErrUnauthenticated, wantStatus: 401, wantCode: "unauthenticated"},
		{name: "forbidden", err: ErrForbidden, wantStatus: 403, wantCode: "forbidden"},
		{name: "not found", err: ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "conflict", err: ErrConflict, wantStatus: 409, wantCode: "conflict"},
		{name: "payload too large", err: ErrPayloadTooLarge, wantStatus: 413, wantCode: "payload_too_large"},
		{name: "validation", err: &ValidationError{Fields: map[string]string{"name": "is required"}}, wantStatus: 422, wantCode: "validation_failed"},
		{name: "rate limited", err: &RateLimitedError{RetryAfter: time.Second}, wantStatus: 429, wantCode: "rate_limited"},
		{name: "unavailable", err: ErrUnavailable, wantStatus: 503, wantCode: "unavailable"},
		{name: "body over the limit", err: &http.MaxBytesError{Limit: 1 << 20}, wantStatus: 413, wantCode: "payload_too_large"},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantStatus: 503, wantCode: "unavailable"},
		{name: "unknown error from a handler", err: unknown, wantStatus: 500, wantCode: "internal"},
		{name: "unknown error from decoding the request", err: unknown, request: true, wantStatus: 400, wantCode: "bad_request"},
		{name: "typed error from decoding the request keeps its type", err: &http.MaxBytesError{Limit: 1}, request: true, wantStatus: 413, wantCode: "payload_too_large"},
		{name: "method not allowed", err: errMethodNotAllowed, wantStatus: 405, wantCode: "bad_request"},
	}

	for _, tt := range tests {
		for _, wrapped := range []bool{false, true} {
			name := tt.name
			err := tt.err
			if wrapped {
				name += " wrapped"
				err = fmt.Errorf("load thing: %w", fmt.Errorf("query: %w", tt.err))
			}
			t.Run(name, func(t *testing.T) {
				rec, _ := respond(t, func(rs *Responder, w http.ResponseWriter, r *http.Request) {
					if tt.request {
						rs.RequestError(w, r, err)
						return
					}
					rs.Error(w, r, err)
				})

				requireEnvelope(t, rec, tt.wantStatus, tt.wantCode)
			})
		}
	}
}

func TestSentinelsMatchWithErrorsIs(t *testing.T) {
	sentinels := []error{
		ErrBadRequest, ErrUnauthenticated, ErrForbidden, ErrNotFound, ErrConflict, ErrPayloadTooLarge, ErrUnavailable,
	}

	for i, sentinel := range sentinels {
		wrapped := fmt.Errorf("context: %w", WithMessage(sentinel, "Specific text."))

		assert.ErrorIs(t, wrapped, sentinel)
		for j, other := range sentinels {
			if i != j {
				assert.NotErrorIs(t, wrapped, other)
			}
		}
	}
}

func TestResponder_Messages(t *testing.T) {
	t.Run("default message", func(t *testing.T) {
		rec, _ := respondError(t, ErrConflict)

		body := requireEnvelope(t, rec, http.StatusConflict, codeConflict)
		assert.Equal(t, "The request conflicts with the current state.", body.Message)
		assert.Nil(t, body.Fields)
	})

	t.Run("WithMessage replaces it", func(t *testing.T) {
		rec, _ := respondError(t, WithMessage(ErrConflict, "This import has already been committed."))

		body := requireEnvelope(t, rec, http.StatusConflict, codeConflict)
		assert.Equal(t, "This import has already been committed.", body.Message)
	})

	t.Run("WithMessage survives further wrapping", func(t *testing.T) {
		err := fmt.Errorf("commit import: %w", WithMessage(ErrConflict, "This import has already been committed."))

		rec, _ := respondError(t, err)

		assert.Equal(t, "This import has already been committed.", requireEnvelope(t, rec, http.StatusConflict, codeConflict).Message)
	})

	t.Run("a message on an unknown error is not shown", func(t *testing.T) {
		rec, _ := respondError(t, WithMessage(errors.New("disk full"), "Leaked text."))

		body := requireEnvelope(t, rec, http.StatusInternalServerError, codeInternal)
		assert.Equal(t, "Something went wrong. Please try again.", body.Message)
	})
}

func TestResponder_Validation(t *testing.T) {
	t.Run("fields and custom message", func(t *testing.T) {
		rec, _ := respondError(t, &ValidationError{
			Message: "Amount must be greater than zero.",
			Fields:  map[string]string{"amount_minor": "must be > 0"},
		})

		body := requireEnvelope(t, rec, http.StatusUnprocessableEntity, codeValidationFailed)
		assert.Equal(t, "Amount must be greater than zero.", body.Message)
		assert.Equal(t, map[string]string{"amount_minor": "must be > 0"}, body.Fields)
	})

	t.Run("default message", func(t *testing.T) {
		rec, _ := respondError(t, &ValidationError{Fields: map[string]string{"name": "is required"}})

		assert.Equal(t, "Some fields are not valid.", requireEnvelope(t, rec, http.StatusUnprocessableEntity, codeValidationFailed).Message)
	})
}

func TestResponder_RateLimitedSetsRetryAfter(t *testing.T) {
	tests := []struct {
		retryAfter time.Duration
		want       string
	}{
		{time.Second, "1"},
		{1200 * time.Millisecond, "2"},
		{90 * time.Second, "90"},
		{0, "1"},
		{-time.Second, "1"},
	}

	for _, tt := range tests {
		t.Run(tt.retryAfter.String(), func(t *testing.T) {
			rec, _ := respondError(t, &RateLimitedError{RetryAfter: tt.retryAfter})

			requireEnvelope(t, rec, http.StatusTooManyRequests, codeRateLimited)
			assert.Equal(t, tt.want, rec.Header().Get("Retry-After"))
		})
	}
}

func TestResponder_OtherErrorsSetNoRetryAfter(t *testing.T) {
	rec, _ := respondError(t, ErrUnavailable)

	assert.Empty(t, rec.Header().Get("Retry-After"))
}

func TestResponder_UnknownErrorIsLoggedNotReturned(t *testing.T) {
	cause := fmt.Errorf("load user: %w", errors.New("pq: password authentication failed for user sprout"))

	rec, logs := respondError(t, cause)

	body := requireEnvelope(t, rec, http.StatusInternalServerError, codeInternal)
	assert.NotContains(t, rec.Body.String(), "password authentication", "the cause must not reach the client")

	line := logs.record(t, "request failed")
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, cause.Error(), line["error"])
	assert.Equal(t, codeInternal, line["code"])
	assert.EqualValues(t, http.StatusInternalServerError, line["status"])
	assert.Equal(t, body.RequestID, line["request_id"])
}

func TestResponder_LogsOnlyServerSideFailures(t *testing.T) {
	t.Run("4xx is not logged", func(t *testing.T) {
		for _, err := range []error{ErrNotFound, ErrConflict, &ValidationError{}, &RateLimitedError{}} {
			_, logs := respondError(t, err)

			assert.Empty(t, logs.records(t), "%v", err)
		}
	})

	t.Run("typed 5xx is logged", func(t *testing.T) {
		_, logs := respondError(t, fmt.Errorf("storage head: %w", ErrUnavailable))

		line := logs.record(t, "request failed")
		assert.Equal(t, codeUnavailable, line["code"])
		assert.Contains(t, line["error"], "storage head")
	})
}

func TestResolve_IsPure(t *testing.T) {
	res := resolve(&RateLimitedError{RetryAfter: 3 * time.Second}, errInternal)

	require.Equal(t, resolution{
		status:            http.StatusTooManyRequests,
		code:              codeRateLimited,
		message:           defaultRateLimitedMessage,
		retryAfterSeconds: 3,
	}, res)
}
