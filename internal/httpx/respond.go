package httpx

import (
	"encoding/json"
	"net/http"
	"time"
)

// WriteJSON writes v as the JSON response body with the given status.
//
// The contract requires optional fields to be present as null rather than
// omitted, so response structs must not use omitempty on them: a nil pointer
// then encodes as null.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding failure can only be a
	// broken connection; there is nothing useful left to do with it.
	_ = json.NewEncoder(w).Encode(v)
}

// NoContent writes a 204 response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Instant returns t as the contract writes instants: UTC, to the second
// (2025-07-21T17:24:00Z). Every timestamp in a response goes through it.
func Instant(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}
