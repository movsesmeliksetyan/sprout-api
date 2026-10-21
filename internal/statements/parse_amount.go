package statements

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// maxAmountMinor bounds one amount: ten trillion minor units.
const maxAmountMinor = 10_000_000_000_000

// errNoAmount is an empty amount cell.
var errNoAmount = errors.New("no amount")

// Sign is the direction an amount cell itself says: negative, positive, or
// marked as a debit or credit (DR, CR).
type Sign int

// The signs an amount can carry.
const (
	SignPositive Sign = iota
	SignNegative
	SignDebit  // marked DR
	SignCredit // marked CR
)

// currencyWords are codes and words that may stand around an amount.
var currencyWords = []string{"EUR", "GBP", "USD", "AMD", "CHF", "RUB", "PLN", "SEK", "NOK", "DKK", "CZK", "HUF", "RON", "TRY", "CAD", "AUD", "JPY", "РУБ", "ДРАМ"}

// ParseAmount reads an amount cell into minor units, exactly: decimal is
// the decimal separator ("." or ","), thousands the grouping one ("",
// ".", ",", " " or "'"; spaces of every width and apostrophes are always
// taken as grouping). It accepts a leading or trailing sign, parentheses
// for negatives, a CR or DR marker, the Unicode minus, and a currency
// symbol or code on either side. At most two decimals count; more are only
// allowed as zeros.
func ParseAmount(cell, decimal, thousands string) (int64, Sign, error) {
	s := strings.TrimSpace(cell)
	if s == "" {
		return 0, SignPositive, errNoAmount
	}
	sign := SignPositive
	upper := strings.ToUpper(s)
	for _, marker := range []struct {
		text string
		sign Sign
	}{{"CR", SignCredit}, {"DR", SignDebit}} {
		if strings.HasSuffix(upper, marker.text) {
			sign, s, upper = marker.sign, strings.TrimSpace(s[:len(s)-len(marker.text)]), strings.TrimSpace(upper[:len(upper)-len(marker.text)])
		}
	}
	for _, word := range currencyWords {
		if strings.HasPrefix(upper, word) {
			s, upper = strings.TrimSpace(s[len(word):]), strings.TrimSpace(upper[len(word):])
		}
		if strings.HasSuffix(upper, word) {
			s, upper = strings.TrimSpace(s[:len(s)-len(word)]), strings.TrimSpace(upper[:len(upper)-len(word)])
		}
	}
	s = strings.TrimFunc(s, func(r rune) bool { return unicode.Is(unicode.Sc, r) || unicode.IsSpace(r) })
	s = strings.NewReplacer("−", "-", "–", "-").Replace(s)

	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		negative, s = true, s[1:len(s)-1]
	}
	s = strings.TrimFunc(s, func(r rune) bool { return unicode.Is(unicode.Sc, r) || unicode.IsSpace(r) })
	switch {
	case strings.HasPrefix(s, "-"):
		negative, s = !negative, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	case strings.HasSuffix(s, "-"):
		negative, s = !negative, s[:len(s)-1]
	}
	s = strings.TrimFunc(s, func(r rune) bool { return unicode.Is(unicode.Sc, r) || unicode.IsSpace(r) })

	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\'' || r == '’' || (thousands != "" && string(r) == thousands && thousands != decimal) {
			return -1
		}
		return r
	}, s)
	whole, fraction, found := strings.Cut(s, decimal)
	if found && strings.Contains(fraction, decimal) {
		return 0, SignPositive, fmt.Errorf("amount %q has two decimal separators", cell)
	}
	if whole == "" && fraction == "" {
		return 0, SignPositive, fmt.Errorf("amount %q has no digits", cell)
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (fraction != "" && !allDigits(fraction)) {
		return 0, SignPositive, fmt.Errorf("amount %q is not a number", cell)
	}
	if len(fraction) > 2 {
		if strings.Trim(fraction[2:], "0") != "" {
			return 0, SignPositive, fmt.Errorf("amount %q has more than two decimals", cell)
		}
		fraction = fraction[:2]
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	if len(whole) > 13 {
		return 0, SignPositive, fmt.Errorf("amount %q is too large", cell)
	}
	var minor int64
	for _, r := range whole + fraction {
		minor = minor*10 + int64(r-'0')
	}
	if minor > maxAmountMinor {
		return 0, SignPositive, fmt.Errorf("amount %q is too large", cell)
	}
	if sign == SignPositive && negative {
		sign = SignNegative
	}
	return minor, sign, nil
}
