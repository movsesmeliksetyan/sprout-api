package llm

import (
	"math/big"
	"regexp"
	"strings"
	"time"
)

// The placeholders Redact leaves where an identifier was.
const (
	MaskEmail   = "[EMAIL]"
	MaskIBAN    = "[IBAN]"
	MaskCard    = "[CARD]"
	MaskAccount = "[ACCOUNT]"
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	// An IBAN is a country code, two check digits and up to 30 letters or
	// digits, often written in groups of four.
	ibanPattern = regexp.MustCompile(`\b[A-Za-z]{2}\d{2}(?:[ ]?[A-Za-z0-9]){11,30}\b`)
	// A run of digits, possibly in groups split by single spaces or dashes:
	// a card number or an account number.
	digitsPattern = regexp.MustCompile(`\b\d(?:[ \-]?\d){7,}\b`)
)

// Redact masks what identifies a person or their money before text is sent
// to a model: email addresses, IBANs, card numbers (13 to 19 digits that
// pass the Luhn check) and account numbers (other runs of 8 or more digits,
// except an 8-digit run that reads as a date, YYYYMMDD or DDMMYYYY).
// Amounts and dates, which a model needs to read a statement, are kept.
// Redact the cells of a row one by one: numbers only a space apart
// read as one.
func Redact(text string) string {
	text = emailPattern.ReplaceAllString(text, MaskEmail)
	text = ibanPattern.ReplaceAllStringFunc(text, maskIBAN)
	return digitsPattern.ReplaceAllStringFunc(text, func(match string) string {
		digits := strings.NewReplacer(" ", "", "-", "").Replace(match)
		switch {
		case len(digits) >= 13 && len(digits) <= 19 && luhn(digits):
			return MaskCard
		case len(digits) == 8 && isDate(digits):
			return match
		}
		return MaskAccount
	})
}

// maskIBAN masks the IBAN a match starts with. The pattern can run on into
// the words after an IBAN written in groups, so the longest run of whole
// groups that passes the check is the IBAN.
func maskIBAN(match string) string {
	groups := strings.Split(match, " ")
	for n := len(groups); n > 0; n-- {
		if validIBAN(strings.Join(groups[:n], "")) {
			return strings.Join(append([]string{MaskIBAN}, groups[n:]...), " ")
		}
	}
	return match
}

// validIBAN applies the ISO 13616 check: with the first four characters
// moved to the end and letters read as 10 to 35, the number is 1 mod 97.
func validIBAN(candidate string) bool {
	iban := strings.ToUpper(strings.ReplaceAll(candidate, " ", ""))
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	var digits strings.Builder
	for _, r := range iban[4:] + iban[:4] {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			digits.WriteString(big.NewInt(int64(r-'A') + 10).String())
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

// luhn reports whether digits pass the check every card number passes.
func luhn(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// isDate reports whether eight digits are a calendar day written YYYYMMDD
// or DDMMYYYY.
func isDate(digits string) bool {
	for _, layout := range []string{"20060102", "02012006"} {
		if day, err := time.Parse(layout, digits); err == nil && day.Year() >= 1900 && day.Year() <= 2100 {
			return true
		}
	}
	return false
}
