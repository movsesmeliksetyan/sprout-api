package statements

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnreadable is a file in a known format whose content cannot be read.
var ErrUnreadable = errors.New("statements: the statement cannot be read")

// ExtractOFX reads the transactions of an OFX or QFX statement, 1.x (SGML,
// where leaf elements are not closed) or 2.x (XML), bank or credit card,
// straight into normalised transactions: OFX says what each field is, so
// no mapping is needed (mapping_source native). Dates without a time are
// midnight in loc.
func ExtractOFX(d Detection, loc *time.Location) ([]NormalizedTransaction, error) {
	elements := ofxElements(d.Text)
	currency := ""
	var out []NormalizedTransaction
	var current map[string]string
	for _, e := range elements {
		switch {
		case e.name == "CURDEF" && currency == "":
			currency = strings.ToUpper(e.value)
		case e.name == "STMTTRN" && e.open:
			current = map[string]string{}
		case e.name == "STMTTRN" && !e.open:
			if current == nil {
				continue
			}
			tx, err := ofxTransaction(current, currency, len(out), loc)
			if err != nil {
				return nil, fmt.Errorf("%w: transaction %d: %w", ErrUnreadable, len(out)+1, err)
			}
			out = append(out, tx)
			current = nil
		case current != nil && e.value != "":
			current[e.name] = e.value
		}
	}
	if current != nil {
		return nil, fmt.Errorf("%w: a transaction is not closed", ErrUnreadable)
	}
	return out, nil
}

// ofxElement is a tag and, for a leaf, the text after it.
type ofxElement struct {
	name  string
	open  bool
	value string
}

// ofxElements tokenises OFX: every tag in order with the text that
// follows it up to the next tag, which in SGML is a leaf's value. XML
// declarations and processing instructions are skipped.
func ofxElements(text string) []ofxElement {
	var out []ofxElement
	for {
		start := strings.IndexByte(text, '<')
		if start < 0 {
			return out
		}
		end := strings.IndexByte(text[start:], '>')
		if end < 0 {
			return out
		}
		tag := text[start+1 : start+end]
		text = text[start+end+1:]
		if strings.HasPrefix(tag, "?") || strings.HasPrefix(tag, "!") {
			continue
		}
		e := ofxElement{name: strings.ToUpper(strings.TrimSpace(tag)), open: true}
		if strings.HasPrefix(e.name, "/") {
			e.name, e.open = e.name[1:], false
		} else {
			next := strings.IndexByte(text, '<')
			if next < 0 {
				next = len(text)
			}
			e.value = unescapeOFX(strings.TrimSpace(text[:next]))
		}
		out = append(out, e)
	}
}

func unescapeOFX(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&nbsp;", " ").Replace(s)
}

// ofxTransaction is one STMTTRN's fields as a transaction.
func ofxTransaction(fields map[string]string, currency string, index int, loc *time.Location) (NormalizedTransaction, error) {
	posted, err := ofxDate(fields["DTPOSTED"], loc)
	if err != nil {
		return NormalizedTransaction{}, err
	}
	amount, negative, err := ofxAmount(fields["TRNAMT"])
	if err != nil {
		return NormalizedTransaction{}, err
	}
	if amount == 0 {
		return NormalizedTransaction{}, errors.New("TRNAMT is zero")
	}
	direction := Credit
	if negative {
		direction = Debit
	}
	if c := fields["CURRENCY"]; c != "" {
		currency = strings.ToUpper(c)
	}
	name, memo := fields["NAME"], fields["MEMO"]
	description := name
	if memo != "" && !strings.EqualFold(memo, name) {
		description = strings.TrimSpace(name + " " + memo)
	}
	return NormalizedTransaction{
		RowIndex:       index,
		PostedAt:       posted,
		AmountMinor:    amount,
		Direction:      direction,
		Currency:       currency,
		RawDescription: description,
		Counterparty:   name,
		MCC:            fields["SIC"],
		ExternalRef:    fields["FITID"],
	}, nil
}

// ofxDate reads YYYYMMDD[HHMMSS[.XXX]][[±H:TZ]]. A date without a time is
// midnight in loc; a time without a zone is in loc too, and an explicit
// offset wins.
func ofxDate(value string, loc *time.Location) (time.Time, error) {
	zone := loc
	if i := strings.IndexByte(value, '['); i >= 0 {
		offset := strings.SplitN(strings.Trim(value[i:], "[]"), ":", 2)[0]
		hours, err := parseSignedInt(offset)
		if err == nil {
			zone = time.FixedZone("", int(hours)*3600)
		}
		value = value[:i]
	}
	if i := strings.IndexByte(value, '.'); i >= 0 {
		value = value[:i]
	}
	switch len(value) {
	case 8:
		date, err := time.ParseInLocation("20060102", value, loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("DTPOSTED %q: %w", value, err)
		}
		return date, nil
	case 12, 14:
		layout := "200601021504"
		if len(value) == 14 {
			layout = "20060102150405"
		}
		at, err := time.ParseInLocation(layout, value, zone)
		if err != nil {
			return time.Time{}, fmt.Errorf("DTPOSTED %q: %w", value, err)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("DTPOSTED %q is not a date", value)
}

// ofxAmount reads a signed decimal amount in minor units: "-42.80",
// "42,8", "+1000". It is exact; no floats.
func ofxAmount(value string) (minor int64, negative bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, errors.New("TRNAMT is missing")
	}
	switch value[0] {
	case '-':
		negative, value = true, value[1:]
	case '+':
		value = value[1:]
	}
	whole, fraction, _ := strings.Cut(strings.ReplaceAll(value, ",", "."), ".")
	if whole == "" {
		whole = "0"
	}
	if len(fraction) > 2 || !allDigits(whole) || (fraction != "" && !allDigits(fraction)) || len(whole) > 15 {
		return 0, false, fmt.Errorf("TRNAMT %q is not an amount", value)
	}
	units, err := parseSignedInt(whole)
	if err != nil {
		return 0, false, err
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	cents, err := parseSignedInt(fraction)
	if err != nil {
		return 0, false, err
	}
	return units*100 + cents, negative, nil
}

// parseSignedInt reads an optionally signed decimal integer.
func parseSignedInt(s string) (int64, error) {
	sign := int64(1)
	switch {
	case strings.HasPrefix(s, "-"):
		sign, s = -1, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !allDigits(s) {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	var n int64
	for _, r := range s {
		n = n*10 + int64(r-'0')
	}
	return sign * n, nil
}
