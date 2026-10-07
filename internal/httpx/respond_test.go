package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalView follows the response convention: optional fields are pointers
// without omitempty.
type goalView struct {
	Title    string  `json:"title"`
	ImageURL *string `json:"image_url"`
	ETAMonth *string `json:"eta_month"`
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusCreated, goalView{Title: "Labubu"})

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestWriteJSON_NilOptionalsAreNull(t *testing.T) {
	eta := "2025-09"
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusOK, goalView{Title: "Labubu", ETAMonth: &eta})

	// A raw comparison, not JSONEq: the key must be present with null, not left out.
	assert.Equal(t, `{"title":"Labubu","image_url":null,"eta_month":"2025-09"}`+"\n", rec.Body.String())
}

func TestNoContent(t *testing.T) {
	rec := httptest.NewRecorder()

	NoContent(rec)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestInstant(t *testing.T) {
	yerevan := time.FixedZone("AMT", 4*60*60)
	in := time.Date(2025, 7, 21, 21, 24, 0, 987_654_321, yerevan)

	encoded, err := json.Marshal(Instant(in))

	require.NoError(t, err)
	assert.Equal(t, `"2025-07-21T17:24:00Z"`, string(encoded))
}
