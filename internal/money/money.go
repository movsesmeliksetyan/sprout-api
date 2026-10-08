// Package money formats minor-unit amounts and provides integer percentage helpers.
package money

import (
	"sort"
	"strconv"
	"strings"
)

// currency describes how amounts in one currency are written in the
// server-rendered English strings (insights, notifications).
type currency struct {
	symbol   string // written before the amount
	decimals int    // minor units per major unit, as a power of ten
}

// currencies holds the supported currencies. Format still writes any other
// code, as the code and two decimals.
var currencies = map[string]currency{
	"USD": {"$", 2},
	"EUR": {"€", 2},
	"GBP": {"£", 2},
	"JPY": {"¥", 0},
	"CAD": {"CA$", 2},
	"AUD": {"A$", 2},
	"AMD": {"֏", 2},
}

// Supported reports whether code is a currency a user may hold their ledger
// in: the ones in the table above, written in upper case.
func Supported(code string) bool {
	_, ok := currencies[code]
	return ok
}

// Format renders an amount of minor units for display: "$1,842.50", "$210".
// Whole amounts drop the decimals; a negative amount leads with a minus sign.
// A currency without a known symbol is written as its code: "CHF 12.50".
func Format(minor int64, currencyCode string) string {
	code := strings.ToUpper(currencyCode)
	cur, known := currencies[code]
	if !known {
		cur = currency{symbol: code + " ", decimals: 2}
	}

	// Work on the magnitude as unsigned so the most negative int64 is safe.
	magnitude := uint64(minor)
	if minor < 0 {
		magnitude = -magnitude
	}
	scale := uint64(1)
	for range cur.decimals {
		scale *= 10
	}
	major, fraction := magnitude/scale, magnitude%scale

	var b strings.Builder
	if minor < 0 {
		b.WriteByte('-')
	}
	b.WriteString(cur.symbol)
	b.WriteString(groupThousands(major))
	if fraction != 0 {
		digits := strconv.FormatUint(fraction, 10)
		b.WriteByte('.')
		b.WriteString(strings.Repeat("0", cur.decimals-len(digits)))
		b.WriteString(digits)
	}
	return b.String()
}

func groupThousands(n uint64) string {
	digits := strconv.FormatUint(n, 10)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	head := len(digits) % 3
	if head > 0 {
		b.WriteString(digits[:head])
	}
	for i := head; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// DivRound returns numerator/denominator rounded to the nearest integer,
// halves away from zero, using integer arithmetic only. It panics when
// denominator is zero, as integer division does.
func DivRound(numerator, denominator int64) int64 {
	if denominator < 0 {
		numerator, denominator = -numerator, -denominator
	}
	quotient, remainder := numerator/denominator, numerator%denominator
	switch {
	case 2*remainder >= denominator:
		return quotient + 1
	case 2*remainder <= -denominator:
		return quotient - 1
	}
	return quotient
}

// Percent returns part as a rounded percentage of total. ok is false when
// total is zero, where the contract sends null.
func Percent(part, total int64) (pct int, ok bool) {
	if total == 0 {
		return 0, false
	}
	return int(DivRound(part*100, total)), true
}

// PercentChange returns the signed, rounded percentage change from previous
// to current. ok is false when previous is zero.
func PercentChange(current, previous int64) (pct int, ok bool) {
	return Percent(current-previous, previous)
}

// Shares splits 100 percentage points across amounts in proportion to each,
// so the result always sums to exactly 100. It uses the largest-remainder
// method: every share is rounded down, then the points left over go to the
// amounts with the largest fractional parts (the larger amount, then the
// earlier one, on a tie).
//
// Negative amounts count as zero. When nothing is positive every share is 0.
func Shares(amounts []int64) []int {
	shares := make([]int, len(amounts))
	var total int64
	for _, a := range amounts {
		if a > 0 {
			total += a
		}
	}
	if total == 0 {
		return shares
	}

	type remainder struct {
		index  int
		amount int64
		rem    int64
	}
	remainders := make([]remainder, 0, len(amounts))
	assigned := 0
	for i, a := range amounts {
		if a <= 0 {
			continue
		}
		shares[i] = int(a * 100 / total)
		assigned += shares[i]
		remainders = append(remainders, remainder{index: i, amount: a, rem: a * 100 % total})
	}

	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].rem != remainders[j].rem {
			return remainders[i].rem > remainders[j].rem
		}
		return remainders[i].amount > remainders[j].amount
	})
	for i := range 100 - assigned {
		shares[remainders[i].index]++
	}
	return shares
}
