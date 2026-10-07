package httpx

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Error codes from the API contract §1.1.
const (
	codeBadRequest       = "bad_request"
	codeUnauthenticated  = "unauthenticated"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeConflict         = "conflict"
	codePayloadTooLarge  = "payload_too_large"
	codeValidationFailed = "validation_failed"
	codeRateLimited      = "rate_limited"
	codeInternal         = "internal"
	codeUnavailable      = "unavailable"
)

// apiError is one row of the contract's error table. message is the default
// text shown to the user.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code }

var (
	errBadRequest      = &apiError{http.StatusBadRequest, codeBadRequest, "The request is not valid."}
	errUnauthenticated = &apiError{http.StatusUnauthorized, codeUnauthenticated, "Sign in to continue."}
	errForbidden       = &apiError{http.StatusForbidden, codeForbidden, "You are not allowed to do that."}
	errNotFound        = &apiError{http.StatusNotFound, codeNotFound, "The requested resource was not found."}
	errConflict        = &apiError{http.StatusConflict, codeConflict, "The request conflicts with the current state."}
	errPayloadTooLarge = &apiError{http.StatusRequestEntityTooLarge, codePayloadTooLarge, "The request body is too large."}
	errUnavailable     = &apiError{http.StatusServiceUnavailable, codeUnavailable, "The service is temporarily unavailable. Please try again."}
	errInternal        = &apiError{http.StatusInternalServerError, codeInternal, "Something went wrong. Please try again."}

	// Temporary: returned by operations whose feature task has not landed. It
	// is not part of the contract and disappears once every operation exists.
	errNotImplemented = &apiError{http.StatusNotImplemented, "not_implemented", "This endpoint is not implemented yet."}

	// The contract has no code for a wrong method, so it reuses bad_request.
	errMethodNotAllowed = &apiError{http.StatusMethodNotAllowed, codeBadRequest, "This method is not allowed for the requested resource."}
)

// Errors a service returns to produce a specific response. Match them with
// errors.Is; attach a specific user-facing message with WithMessage.
var (
	ErrBadRequest      error = errBadRequest
	ErrUnauthenticated error = errUnauthenticated
	ErrForbidden       error = errForbidden
	ErrNotFound        error = errNotFound
	ErrConflict        error = errConflict
	ErrPayloadTooLarge error = errPayloadTooLarge
	ErrUnavailable     error = errUnavailable
)

const (
	defaultValidationMessage  = "Some fields are not valid."
	defaultRateLimitedMessage = "Too many requests. Please try again shortly."
)

// ValidationError reports invalid input, keyed by field name as it appears
// on the wire. Message and every value in Fields are shown to the user.
type ValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *ValidationError) Error() string { return codeValidationFailed }

// RateLimitedError tells the caller to retry after a delay.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string { return codeRateLimited }

type messageError struct {
	err     error
	message string
}

func (e *messageError) Error() string { return e.err.Error() + ": " + e.message }
func (e *messageError) Unwrap() error { return e.err }

// WithMessage returns err carrying message as the text shown to the user in
// place of the default. The result still matches err with errors.Is.
func WithMessage(err error, message string) error {
	return &messageError{err: err, message: message}
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields"`
	RequestID string            `json:"request_id"`
}

// resolution is what an error becomes on the wire.
type resolution struct {
	status            int
	code              string
	message           string
	fields            map[string]string
	retryAfterSeconds int // 0 means no Retry-After header
}

// resolve maps err to its response. unknown is used for an error that is
// none of the typed ones.
func resolve(err error, unknown *apiError) resolution {
	var validation *ValidationError
	if errors.As(err, &validation) {
		message := validation.Message
		if message == "" {
			message = defaultValidationMessage
		}
		return resolution{
			status:  http.StatusUnprocessableEntity,
			code:    codeValidationFailed,
			message: message,
			fields:  validation.Fields,
		}
	}

	var limited *RateLimitedError
	if errors.As(err, &limited) {
		return resolution{
			status:            http.StatusTooManyRequests,
			code:              codeRateLimited,
			message:           defaultRateLimitedMessage,
			retryAfterSeconds: max(1, int(math.Ceil(limited.RetryAfter.Seconds()))),
		}
	}

	var api *apiError
	if !errors.As(err, &api) {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			api = errPayloadTooLarge
		case errors.Is(err, context.DeadlineExceeded):
			api = errUnavailable
		default:
			api = unknown
		}
	}

	message := api.message
	var custom *messageError
	// An internal error never carries text from the error chain.
	if api != errInternal && errors.As(err, &custom) {
		message = custom.message
	}
	return resolution{status: api.status, code: api.code, message: message}
}

// Responder writes errors as the contract's envelope. It is the only place
// that turns an error into a status and code.
type Responder struct {
	logger *slog.Logger
}

// NewResponder returns a Responder that logs server-side failures to logger.
func NewResponder(logger *slog.Logger) *Responder {
	return &Responder{logger: logger}
}

// Error writes the response for an error returned by a handler or service.
// An error that is not one of the typed ones becomes 500 internal: its text
// is logged and never sent to the client.
func (rs *Responder) Error(w http.ResponseWriter, r *http.Request, err error) {
	rs.write(w, r, err, errInternal)
}

// RequestError writes the response for a failure to read or decode the
// request. An error that is not one of the typed ones becomes 400 bad_request.
func (rs *Responder) RequestError(w http.ResponseWriter, r *http.Request, err error) {
	rs.write(w, r, err, errBadRequest)
}

func (rs *Responder) write(w http.ResponseWriter, r *http.Request, err error, unknown *apiError) {
	res := resolve(err, unknown)
	// Client errors already show in the access log; server-side ones need the
	// cause. A not-implemented answer is expected, not a failure.
	if res.status >= http.StatusInternalServerError && res.status != http.StatusNotImplemented {
		rs.logger.ErrorContext(r.Context(), "request failed",
			slog.Int("status", res.status),
			slog.String("code", res.code),
			slog.String("error", err.Error()),
		)
	}
	writeResolution(w, r, res)
}

// writeResolution writes the envelope without logging, for callers that have
// already logged the cause themselves.
func writeResolution(w http.ResponseWriter, r *http.Request, res resolution) {
	if res.retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(res.retryAfterSeconds))
	}
	WriteJSON(w, res.status, errorEnvelope{Error: errorBody{
		Code:      res.code,
		Message:   res.message,
		Fields:    res.fields,
		RequestID: RequestID(r.Context()),
	}})
}
