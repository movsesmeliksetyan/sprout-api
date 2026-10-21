package statements

import (
	"errors"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// Validation limits (PRD §4.3 stage 5).
const (
	// MinParseRate is the share of rows, in percent, that must be read for
	// an import to go on.
	MinParseRate = 50
	// MaxRows is the most rows an import keeps.
	MaxRows = 5000
	// reconcileRate is the share of balance steps, in percent, that the
	// flipped sign convention must explain, where the file's explains
	// fewer, for the rows to be flipped.
	reconcileRate = 80
	// minBalanceSteps is how many steps between balances it takes to judge
	// the sign convention.
	minBalanceSteps = 2
)

// ErrLowParseRate is a file whose rows mostly could not be read.
var ErrLowParseRate = errors.New("statements: too few rows could be read")

// Window is the period of transactions an import keeps; the zero Window
// keeps all of them.
type Window struct {
	Range period.Range
}

// NewWindow is the current period of the kind ("week", "month", "year" or
// "all") as seen in loc at now.
func NewWindow(kind string, loc *time.Location, now time.Time) (Window, error) {
	if kind == "all" {
		return Window{}, nil
	}
	k, err := period.ParseKind(kind)
	if err != nil {
		return Window{}, err
	}
	r, err := period.Resolve(k, 0, loc, now)
	if err != nil {
		return Window{}, err
	}
	return Window{Range: r}, nil
}

func (w Window) contains(posted time.Time, loc *time.Location) bool {
	return w.Range.Start.IsZero() || w.Range.Contains(period.LocalDate(posted, loc))
}

// Validated is what Validate kept of a file's rows.
type Validated struct {
	Rows []Row
	// OutOfPeriod counts the readable rows outside the window, dropped.
	OutOfPeriod int
	// Flipped is set when the balances showed the file's sign convention
	// was the other way round, and every row's direction was turned.
	Flipped bool
}

// Validate checks a file's rows and keeps those in the window (PRD §4.3
// stage 5). No rows is ErrNoRows, fewer than MinParseRate % readable is
// ErrLowParseRate. Where the file has running balances that the rows'
// directions consistently contradict and the opposite directions explain,
// the directions are flipped. Readable rows outside the window are dropped
// and counted; rows that could not be read are kept, to be shown as
// errors. More than MaxRows left is ErrTooManyRows.
func Validate(rows []Row, window Window, loc *time.Location) (Validated, error) {
	if len(rows) == 0 {
		return Validated{}, ErrNoRows
	}
	if ParseRate(rows) < MinParseRate {
		return Validated{}, ErrLowParseRate
	}
	var result Validated
	rows = append([]Row(nil), rows...)
	if needsFlip(rows) {
		for i := range rows {
			if rows[i].Error == "" {
				rows[i].Direction = opposite(rows[i].Direction)
			}
		}
		result.Flipped = true
	}
	for _, row := range rows {
		if row.Error == "" && !window.contains(row.PostedAt, loc) {
			result.OutOfPeriod++
			continue
		}
		result.Rows = append(result.Rows, row)
	}
	if len(result.Rows) > MaxRows {
		return Validated{}, fmt.Errorf("%w: %d rows, at most %d are kept", ErrTooManyRows, len(result.Rows), MaxRows)
	}
	return result, nil
}

// needsFlip reports whether the running balances say the directions are
// the wrong way round: each step between two neighbouring readable rows
// with balances is checked against both the file's directions and the
// opposite ones, in either row order (statements list oldest or newest
// first). The rows are flipped only when the opposite directions explain
// at least reconcileRate % of the steps and more of them than the file's.
func needsFlip(rows []Row) bool {
	var steps, asIs, flipped int
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if prev.Error != "" || cur.Error != "" || prev.BalanceAfter == nil || cur.BalanceAfter == nil {
			continue
		}
		steps++
		delta := *cur.BalanceAfter - *prev.BalanceAfter
		// Oldest first, cur moved the balance; newest first, prev did.
		if delta == signed(cur) || -delta == signed(prev) {
			asIs++
		}
		if delta == -signed(cur) || -delta == -signed(prev) {
			flipped++
		}
	}
	return steps >= minBalanceSteps && flipped > asIs && flipped*100 >= steps*reconcileRate
}

// signed is a row's effect on the balance.
func signed(row Row) int64 {
	if row.Direction == Debit {
		return -row.AmountMinor
	}
	return row.AmountMinor
}

func opposite(d Direction) Direction {
	if d == Debit {
		return Credit
	}
	return Debit
}
