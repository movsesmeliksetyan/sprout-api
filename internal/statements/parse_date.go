package statements

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// dateTokens are what a date format is made of (contract §2.10).
var dateTokens = []string{"YYYY", "MMM", "DD", "MM", "YY"}

// monthNames maps month names and abbreviations, lower case and without
// accents (folded as foldMonth folds a cell), to months: English, German, French, Spanish, Italian, Dutch,
// Portuguese, Polish, Russian and Armenian.
var monthNames = func() map[string]time.Month {
	names := [12][]string{
		{"jan", "january", "januar", "janv", "janvier", "ene", "enero", "gen", "gennaio", "januari", "janeiro", "sty", "styczen", "stycznia", "янв", "январь", "января", "հնվ", "հունվար", "հունվարի"},
		{"feb", "february", "februar", "fevr", "fevrier", "febrero", "febbraio", "februari", "fev", "fevereiro", "lut", "luty", "lutego", "фев", "февраль", "февраля", "փտվ", "փետրվար", "փետրվարի"},
		{"mar", "march", "marz", "maerz", "mars", "marzo", "mrt", "maart", "marco", "marzec", "marca", "мар", "март", "марта", "մրտ", "մարտ", "մարտի"},
		{"apr", "april", "avr", "avril", "abr", "abril", "aprile", "kwi", "kwiecien", "kwietnia", "апр", "апрель", "апреля", "ապր", "ապրիլ", "ապրիլի"},
		{"may", "mai", "mayo", "mag", "maggio", "mei", "maio", "maj", "maja", "май", "мая", "մյս", "մայիս", "մայիսի"},
		{"jun", "june", "juni", "juin", "junio", "giu", "giugno", "junho", "cze", "czerwiec", "czerwca", "июн", "июнь", "июня", "հնս", "հունիս", "հունիսի"},
		{"jul", "july", "juli", "juil", "juillet", "julio", "lug", "luglio", "julho", "lip", "lipiec", "lipca", "июл", "июль", "июля", "հլս", "հուլիս", "հուլիսի"},
		{"aug", "august", "aout", "ago", "agosto", "augustus", "sie", "sierpien", "sierpnia", "авг", "август", "августа", "օգս", "օգոստոս", "օգոստոսի"},
		{"sep", "sept", "september", "septembre", "sep", "septiembre", "set", "settembre", "setembro", "wrz", "wrzesien", "wrzesnia", "сен", "сентябрь", "сентября", "սեպ", "սեպտեմբեր", "սեպտեմբերի"},
		{"oct", "october", "okt", "oktober", "octobre", "octubre", "ott", "ottobre", "out", "outubro", "paz", "pazdziernik", "pazdziernika", "окт", "октябрь", "октября", "հոկ", "հոկտեմբեր", "հոկտեմբերի"},
		{"nov", "november", "novembre", "noviembre", "novembro", "lis", "listopad", "listopada", "ноя", "ноябрь", "ноября", "նոյ", "նոյեմբեր", "նոյեմբերի"},
		{"dec", "december", "dez", "dezember", "dec", "decembre", "dic", "diciembre", "dicembre", "dezembro", "gru", "grudzien", "grudnia", "дек", "декабрь", "декабря", "դեկ", "դեկտեմբեր", "դեկտեմբերի"}, //nolint:misspell // French "décembre", accents folded
	}
	m := make(map[string]time.Month)
	for i, list := range names {
		for _, name := range list {
			m[foldMonth(name)] = time.Month(i + 1)
		}
	}
	return m
}()

// ValidDateFormat reports whether a format is tokens of contract §2.10
// with at most separators between them, holding a day, a month and a year
// once each.
func ValidDateFormat(format string) bool {
	tokens, ok := dateFormatTokens(format)
	if !ok {
		return false
	}
	seen := map[byte]int{}
	for _, t := range tokens {
		seen[t[0]]++
	}
	return seen['D'] == 1 && seen['M'] == 1 && seen['Y'] == 1
}

// dateFormatTokens splits a format into its tokens, dropping separators.
func dateFormatTokens(format string) ([]string, bool) {
	var tokens []string
	for rest := strings.ToUpper(format); rest != ""; {
		matched := false
		for _, token := range dateTokens {
			if strings.HasPrefix(rest, token) {
				tokens = append(tokens, token)
				rest = rest[len(token):]
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if !strings.ContainsRune("./- ,", rune(rest[0])) {
			return nil, false
		}
		rest = rest[1:]
	}
	return tokens, len(tokens) > 0
}

// ParseDate reads a date cell with a format of contract §2.10 (DD, MM,
// YYYY, YY, MMM; any of ./- space between them, or none) and returns
// midnight of that day in loc. A time after the date is ignored. YY is
// 20YY. Month names are matched in ten languages, case and accents aside.
func ParseDate(cell, format string, loc *time.Location) (time.Time, error) {
	tokens, ok := dateFormatTokens(format)
	if !ok {
		return time.Time{}, fmt.Errorf("date format %q is not valid", format)
	}
	parts := dateParts(cell)
	if len(parts) == 1 && allDigits(parts[0]) && len(tokens) > 1 {
		parts = splitCompact(parts[0], tokens)
	}
	if len(parts) < len(tokens) {
		return time.Time{}, fmt.Errorf("date %q does not match %s", cell, format)
	}
	var year, day int
	var month time.Month
	for i, token := range tokens {
		part := parts[i]
		if token == "MMM" {
			m, ok := monthNames[foldMonth(part)]
			if !ok {
				return time.Time{}, fmt.Errorf("date %q: %q is not a month", cell, part)
			}
			month = m
			continue
		}
		if !allDigits(part) {
			return time.Time{}, fmt.Errorf("date %q does not match %s", cell, format)
		}
		n := 0
		for _, r := range part {
			n = n*10 + int(r-'0')
		}
		switch token {
		case "DD":
			if len(part) > 2 {
				return time.Time{}, fmt.Errorf("date %q: %q is not a day", cell, part)
			}
			day = n
		case "MM":
			if len(part) > 2 {
				return time.Time{}, fmt.Errorf("date %q: %q is not a month", cell, part)
			}
			month = time.Month(n)
		case "YYYY":
			if len(part) != 4 {
				return time.Time{}, fmt.Errorf("date %q: %q is not a year", cell, part)
			}
			year = n
		case "YY":
			if len(part) != 2 {
				return time.Time{}, fmt.Errorf("date %q: %q is not a year", cell, part)
			}
			year = 2000 + n
		}
	}
	if month < 1 || month > 12 || day < 1 || year < 1900 || year > 2200 {
		return time.Time{}, fmt.Errorf("date %q is not a date", cell)
	}
	date := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if date.Day() != day || date.Month() != month {
		return time.Time{}, fmt.Errorf("date %q is not a date", cell)
	}
	return date, nil
}

// dateParts splits a cell into runs of digits and runs of letters.
func dateParts(cell string) []string {
	var parts []string
	var b strings.Builder
	kind := 0 // 1 digits, 2 letters
	flush := func() {
		if b.Len() > 0 {
			parts = append(parts, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.TrimSpace(cell) {
		k := 0
		switch {
		case unicode.IsDigit(r):
			k = 1
		case unicode.IsLetter(r):
			k = 2
		}
		if k != kind {
			flush()
			kind = k
		}
		if k != 0 {
			b.WriteRune(r)
		}
	}
	flush()
	return parts
}

// splitCompact cuts a run of digits such as 20250714 by the widths of the
// format's tokens.
func splitCompact(digits string, tokens []string) []string {
	widths := map[string]int{"YYYY": 4, "YY": 2, "MM": 2, "DD": 2}
	var parts []string
	for _, token := range tokens {
		w, ok := widths[token]
		if !ok || len(digits) < w {
			return []string{digits}
		}
		parts = append(parts, digits[:w])
		digits = digits[w:]
	}
	return append(parts, digits)
}

// foldMonth lower-cases a month name and drops accents and a final dot.
func foldMonth(name string) string {
	decomposed := norm.NFD.String(strings.ToLower(strings.TrimSuffix(name, ".")))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)
}
