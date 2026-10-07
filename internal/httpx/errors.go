package httpx

import (
	"encoding/json"
	"net/http"
)

// Error codes from the API contract §1.1.
const (
	codeBadRequest      = "bad_request"
	codeNotFound        = "not_found"
	codePayloadTooLarge = "payload_too_large"
	codeInternal        = "internal"
	codeUnavailable     = "unavailable"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields"`
	RequestID string            `json:"request_id"`
}

// writeError writes the error envelope. message must be safe to show to the user.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{
		Code:      code,
		Message:   message,
		RequestID: RequestID(r.Context()),
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding failure can only be a
	// broken connection; there is nothing useful left to do with it.
	_ = json.NewEncoder(w).Encode(v)
}
