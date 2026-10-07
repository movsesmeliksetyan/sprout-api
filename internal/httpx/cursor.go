package httpx

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	cursorDateLayout = "2006-01-02"
	cursorSeparator  = "|"
)

var errInvalidCursor = WithMessage(ErrBadRequest, "The cursor is not valid.")

// Cursor is a position in a list ordered by (local_date DESC, id DESC).
// LocalDate is a calendar day held as midnight UTC.
type Cursor struct {
	LocalDate time.Time
	ID        uuid.UUID
}

// EncodeCursor returns the opaque form of c sent to clients as next_cursor.
func EncodeCursor(c Cursor) string {
	raw := c.LocalDate.Format(cursorDateLayout) + cursorSeparator + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor received from a client. Anything that
// EncodeCursor could not have produced is rejected with ErrBadRequest.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errInvalidCursor
	}
	date, id, found := strings.Cut(string(raw), cursorSeparator)
	if !found {
		return Cursor{}, errInvalidCursor
	}
	localDate, err := time.Parse(cursorDateLayout, date)
	if err != nil {
		return Cursor{}, errInvalidCursor
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, errInvalidCursor
	}

	c := Cursor{LocalDate: localDate, ID: parsedID}
	// The decoders above are lenient (alternative UUID spellings, stray base64
	// bits); only the canonical encoding is accepted.
	if EncodeCursor(c) != s {
		return Cursor{}, errInvalidCursor
	}
	return c, nil
}
