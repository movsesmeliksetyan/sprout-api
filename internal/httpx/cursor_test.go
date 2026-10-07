package httpx

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCursorID = "0192f0c8-7b1a-7c3e-9d2f-4a5b6c7d8e9f"

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func encodeRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestCursor_RoundTrips(t *testing.T) {
	cursors := []Cursor{
		{LocalDate: day(2025, time.July, 21), ID: uuid.MustParse(testCursorID)},
		{LocalDate: day(2024, time.February, 29), ID: uuid.Must(uuid.NewV7())},
		{LocalDate: day(1999, time.December, 31), ID: uuid.Nil},
		{LocalDate: day(2026, time.January, 1), ID: uuid.Max},
	}

	for _, want := range cursors {
		t.Run(want.LocalDate.Format(cursorDateLayout), func(t *testing.T) {
			encoded := EncodeCursor(want)

			got, err := DecodeCursor(encoded)

			require.NoError(t, err)
			assert.True(t, want.LocalDate.Equal(got.LocalDate), "date: want %s, got %s", want.LocalDate, got.LocalDate)
			assert.Equal(t, time.UTC, got.LocalDate.Location())
			assert.Equal(t, want.ID, got.ID)
			assert.NotContains(t, encoded, "|", "the cursor is opaque")
			assert.NotRegexp(t, `[+/=]`, encoded, "the cursor is safe in a query string")
		})
	}
}

func TestCursor_EncodesDateAndID(t *testing.T) {
	c := Cursor{LocalDate: day(2025, time.July, 21), ID: uuid.MustParse(testCursorID)}

	assert.Equal(t, encodeRaw("2025-07-21|"+testCursorID), EncodeCursor(c))
}

func TestDecodeCursor_RejectsTampered(t *testing.T) {
	valid := encodeRaw("2025-07-21|" + testCursorID)

	tests := []struct {
		name   string
		cursor string
	}{
		{"empty", ""},
		{"not base64", "!!!not-base64!!!"},
		{"standard base64 alphabet", base64.StdEncoding.EncodeToString([]byte("2025-07-21|" + testCursorID + "??>"))},
		{"padded", valid + "="},
		{"truncated", valid[:len(valid)-5]},
		{"trailing characters", valid + "AAAA"},
		{"embedded newline", valid[:8] + "\n" + valid[8:]},
		{"last character changed", valid[:len(valid)-1] + "x"},
		{"empty payload", encodeRaw("")},
		{"no separator", encodeRaw("2025-07-21" + testCursorID)},
		{"two separators", encodeRaw("2025-07-21|" + testCursorID + "|extra")},
		{"missing id", encodeRaw("2025-07-21|")},
		{"missing date", encodeRaw("|" + testCursorID)},
		{"impossible date", encodeRaw("2025-02-30|" + testCursorID)},
		{"wrong date format", encodeRaw("21/07/2025|" + testCursorID)},
		{"date with time", encodeRaw("2025-07-21T00:00:00Z|" + testCursorID)},
		{"id is not a uuid", encodeRaw("2025-07-21|not-a-uuid")},
		{"uuid without dashes", encodeRaw("2025-07-21|0192f0c87b1a7c3e9d2f4a5b6c7d8e9f")},
		{"uuid in braces", encodeRaw("2025-07-21|{" + testCursorID + "}")},
		{"uuid upper case", encodeRaw("2025-07-21|0192F0C8-7B1A-7C3E-9D2F-4A5B6C7D8E9F")},
		{"sql in the id", encodeRaw("2025-07-21|'; DROP TABLE transactions;--")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeCursor(tt.cursor)

			require.ErrorIs(t, err, ErrBadRequest)
			assert.Equal(t, Cursor{}, got)
		})
	}
}

func TestDecodeCursor_TamperedCursorRespondsBadRequest(t *testing.T) {
	_, err := DecodeCursor("tampered")
	require.Error(t, err)

	rec, _ := respondError(t, err)

	body := requireEnvelope(t, rec, http.StatusBadRequest, codeBadRequest)
	assert.Equal(t, "The cursor is not valid.", body.Message)
}
