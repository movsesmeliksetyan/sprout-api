package statements

import (
	"strings"
	"unicode"
)

// Hints is what is known about a statement beyond its table.
type Hints struct {
	// MonthFirst says dates such as 04/07 are 7 April (US style) when the
	// values cannot tell; nil leaves them day first, with less confidence.
	MonthFirst *bool
}

// Confidence levels for the parts of a heuristic mapping, in percent.
const (
	confidenceHeader  = 100 // a header names the role
	confidencePartial = 90  // a header holds a label for the role
	confidenceValues  = 70  // the values alone suggest the role
	confidenceHinted  = 85  // a date order taken from a hint
	confidenceGuessed = 60  // a date order guessed
)

// HeuristicThreshold is the confidence, in percent, a heuristic mapping
// needs to be used without asking the model.
const HeuristicThreshold = 80

// column is what the values of one column look like.
type column struct {
	index     int
	header    string
	role      role
	strength  int
	dates     int // values that look like dates
	numbers   int // values that look like amounts
	negatives int // amounts with a minus or parentheses
	text      int // values with letters and not dates
	markers   int // debit/credit markers
	values    int // non-empty values
	distinct  int
}

// Heuristic proposes a mapping for a table from its headers and values
// (PRD §4.3 stage 3b) and says how confident it is, in percent. A spec it
// cannot complete comes back with confidence 0.
func Heuristic(table RawTable, hints Hints) (MappingSpec, int) {
	syn, err := loadSynonyms()
	if err != nil || len(table.Headers) == 0 || len(table.Rows) == 0 {
		return MappingSpec{}, 0
	}
	columns := profile(table, syn)
	spec := MappingSpec{AmountSign: NegativeIsExpense}
	confidence := confidenceHeader
	lower := func(c int) { confidence = min(confidence, c) }

	// Date: the column a header names whose values are dates; else the
	// column most of whose values are dates.
	date := pick(columns, func(c column) bool { return c.role == roleDate && c.dates*10 >= c.values*9 })
	if date == nil {
		date = best(columns, func(c column) int {
			if c.role == roleIgnore || c.dates*10 < c.values*9 {
				return 0
			}
			return c.dates
		})
		lower(confidenceValues)
	} else {
		lower(strengthConfidence(date.strength))
	}
	if date == nil {
		return MappingSpec{}, 0
	}
	spec.Columns.Date = date.index
	format, formatConfidence := inferDateFormat(columnValues(table, date.index), hints)
	if format == "" {
		return MappingSpec{}, 0
	}
	spec.DateFormat = format
	lower(formatConfidence)

	taken := map[int]bool{date.index: true}
	numeric := func(c column) bool { return c.values > 0 && c.numbers*10 >= c.values*9 }
	claim := func(r role, ok func(column) bool) *column {
		c := pick(columns, func(c column) bool { return !taken[c.index] && c.role == r && ok(c) })
		if c != nil {
			taken[c.index] = true
		}
		return c
	}

	// Amount: one signed column, or a debit and a credit column.
	if amount := claim(roleAmount, numeric); amount != nil {
		spec.Columns.Amount = &amount.index
		lower(strengthConfidence(amount.strength))
	} else {
		debit, credit := claim(roleDebit, numeric), claim(roleCredit, numeric)
		switch {
		case debit != nil && credit != nil:
			spec.Columns.Debit, spec.Columns.Credit = &debit.index, &credit.index
			lower(min(strengthConfidence(debit.strength), strengthConfidence(credit.strength)))
		default:
			// The values decide: a numeric column with both signs, not a
			// balance.
			signed := best(columns, func(c column) int {
				if taken[c.index] || c.role == roleBalance || c.role == roleIgnore || !numeric(c) || c.negatives == 0 || c.negatives == c.values {
					return 0
				}
				return c.numbers
			})
			if signed == nil {
				return MappingSpec{}, 0
			}
			taken[signed.index] = true
			spec.Columns.Amount = &signed.index
			lower(confidenceValues)
		}
	}
	if balance := claim(roleBalance, numeric); balance != nil {
		spec.Columns.Balance = &balance.index
	}
	if currency := claim(roleCurrency, func(column) bool { return true }); currency != nil {
		spec.Columns.Currency = &currency.index
	}
	if reference := claim(roleReference, func(column) bool { return true }); reference != nil {
		spec.Columns.Reference = &reference.index
	}
	if mcc := claim(roleMCC, func(column) bool { return true }); mcc != nil {
		spec.Columns.MCC = &mcc.index
	}
	// A column of debit/credit markers says the direction of an unsigned
	// amount; it is never a description.
	if marker := pick(columns, func(c column) bool { return !taken[c.index] && c.values >= 2 && c.markers == c.values }); marker != nil {
		taken[marker.index] = true
		if spec.Columns.Amount != nil {
			spec.Columns.Direction = &marker.index
		}
	}

	// Description: every column a header names as one; else the column of
	// text with the most different values.
	for _, c := range columns {
		if !taken[c.index] && c.role == roleDescription && c.text > 0 {
			spec.Columns.Description = append(spec.Columns.Description, c.index)
			taken[c.index] = true
			lower(strengthConfidence(c.strength))
		}
	}
	if len(spec.Columns.Description) == 0 {
		text := best(columns, func(c column) int {
			if taken[c.index] || c.role == roleIgnore || c.text*2 < c.values {
				return 0
			}
			return c.distinct
		})
		if text == nil {
			return MappingSpec{}, 0
		}
		spec.Columns.Description = []int{text.index}
		lower(confidenceValues)
	}

	spec.DecimalSeparator, spec.ThousandsSeparator = inferSeparators(table, spec)
	return spec, confidence
}

func strengthConfidence(strength int) int {
	if strength == 2 {
		return confidenceHeader
	}
	return confidencePartial
}

// profile describes every column of a table.
func profile(table RawTable, syn *synonyms) []column {
	columns := make([]column, len(table.Headers))
	for i, header := range table.Headers {
		r, strength := syn.match(header)
		columns[i] = column{index: i, header: header, role: r, strength: strength}
		seen := map[string]bool{}
		for _, row := range table.Rows {
			if i >= len(row) {
				continue
			}
			value := strings.TrimSpace(row[i])
			if value == "" {
				continue
			}
			c := &columns[i]
			c.values++
			if !seen[value] {
				seen[value] = true
				c.distinct++
			}
			isDate := looksDate(value) || isISODateTime(value)
			if isDate {
				c.dates++
			}
			if !isDate && looksAmount(value) {
				c.numbers++
				if strings.ContainsAny(value, "-(−") {
					c.negatives++
				}
			}
			if !isDate && strings.IndexFunc(value, unicode.IsLetter) >= 0 {
				c.text++
			}
			if _, ok := directionMarker(value); ok {
				c.markers++
			}
		}
	}
	return columns
}

// looksAmount reports whether a cell reads as an amount (ParseAmount takes
// it with one of the two separator conventions).
func looksAmount(value string) bool {
	if _, _, err := ParseAmount(value, ".", ","); err == nil {
		return true
	}
	_, _, err := ParseAmount(value, ",", ".")
	return err == nil
}

// isISODateTime reports whether a cell is an ISO date with a time.
func isISODateTime(value string) bool {
	date, _, found := strings.Cut(value, "T")
	return found && len(date) == 10 && looksDate(date)
}

// pick is the first column ok accepts, preferring exact header matches.
func pick(columns []column, ok func(column) bool) *column {
	var partial *column
	for i := range columns {
		if !ok(columns[i]) {
			continue
		}
		if columns[i].strength == 2 {
			return &columns[i]
		}
		if partial == nil {
			partial = &columns[i]
		}
	}
	return partial
}

// best is the column with the highest positive score, the first on a tie.
func best(columns []column, score func(column) int) *column {
	var out *column
	top := 0
	for i := range columns {
		if s := score(columns[i]); s > top {
			out, top = &columns[i], s
		}
	}
	return out
}

func columnValues(table RawTable, index int) []string {
	var values []string
	for _, row := range table.Rows {
		if index < len(row) {
			if v := strings.TrimSpace(row[index]); v != "" {
				values = append(values, v)
			}
		}
	}
	return values
}

// inferDateFormat works out a date column's format from its values: where
// the year is, whether the month is a name, and the order of day and
// month — from a value over 12, else the hint, else day first with low
// confidence.
func inferDateFormat(values []string, hints Hints) (string, int) {
	if len(values) == 0 {
		return "", 0
	}
	sample := values[0]
	parts := dateParts(sample)
	if len(parts) == 1 && len(parts[0]) == 8 && allDigits(parts[0]) {
		if looksDate(parts[0]) {
			return "YYYYMMDD", confidenceHeader
		}
		return "DDMMYYYY", confidenceValues
	}
	if len(parts) < 3 {
		return "", 0
	}
	parts = parts[:3]
	tokens := make([]string, 3)
	yearAt, nameAt := -1, -1
	for i, part := range parts {
		switch {
		case !allDigits(part):
			nameAt = i
			tokens[i] = "MMM"
		case len(part) == 4:
			yearAt = i
			tokens[i] = "YYYY"
		}
	}
	if yearAt < 0 {
		yearAt = 2 // a two-digit year comes last
		tokens[2] = "YY"
	}
	confidence := confidenceHeader
	var numeric []int
	for i := range tokens {
		if tokens[i] == "" {
			numeric = append(numeric, i)
		}
	}
	switch {
	case nameAt >= 0 && len(numeric) == 1:
		tokens[numeric[0]] = "DD"
	case len(numeric) == 2:
		first, second := numeric[0], numeric[1]
		dayFirst := yearAt != 0 // year first is ISO: YYYY-MM-DD
		switch {
		case exceeds12(values, first):
			dayFirst = true
		case exceeds12(values, second):
			dayFirst = false
		case yearAt == 0:
		case hints.MonthFirst != nil:
			dayFirst = !*hints.MonthFirst
			confidence = confidenceHinted
		default:
			confidence = confidenceGuessed
		}
		if dayFirst {
			tokens[first], tokens[second] = "DD", "MM"
		} else {
			tokens[first], tokens[second] = "MM", "DD"
		}
	default:
		return "", 0
	}
	return joinDateTokens(sample, tokens), confidence
}

// exceeds12 reports whether any value has a number over 12 in the given
// part: it must be the day.
func exceeds12(values []string, part int) bool {
	for _, v := range values {
		parts := dateParts(v)
		if part < len(parts) && allDigits(parts[part]) {
			n := 0
			for _, r := range parts[part] {
				n = n*10 + int(r-'0')
			}
			if n > 12 {
				return true
			}
		}
	}
	return false
}

// joinDateTokens writes the tokens with the separators the sample has
// between its parts.
func joinDateTokens(sample string, tokens []string) string {
	var separators []string
	var current strings.Builder
	inPart := false
	for _, r := range strings.TrimSpace(sample) {
		if unicode.IsDigit(r) || unicode.IsLetter(r) {
			if !inPart && len(separators) < len(tokens) && current.Len() > 0 {
				separators = append(separators, current.String())
				current.Reset()
			}
			inPart = true
			continue
		}
		if inPart && len(separators) >= len(tokens)-1 {
			break
		}
		inPart = false
		current.WriteRune(r)
	}
	var b strings.Builder
	for i, token := range tokens {
		if i > 0 {
			sep := " "
			if i-1 < len(separators) {
				sep = separators[i-1]
			}
			b.WriteString(sep)
		}
		b.WriteString(token)
	}
	return b.String()
}

// inferSeparators reads the decimal and thousands separators from a spec's
// amount and balance columns: the last of "." and "," followed by one or
// two digits is the decimal one; a value with both makes the other the
// thousands one, as do spaces or apostrophes between digits.
func inferSeparators(table RawTable, spec MappingSpec) (string, string) {
	votes := map[string]int{}
	thousands := ""
	for _, index := range []*int{spec.Columns.Amount, spec.Columns.Debit, spec.Columns.Credit, spec.Columns.Balance} {
		if index == nil {
			continue
		}
		for _, value := range columnValues(table, *index) {
			last := strings.LastIndexAny(value, ".,")
			if last < 0 {
				continue
			}
			digits := 0
			for _, r := range value[last+1:] {
				if !unicode.IsDigit(r) {
					break
				}
				digits++
			}
			sep := value[last : last+1]
			other := map[string]string{".": ",", ",": "."}[sep]
			switch digits {
			case 1, 2:
				votes[sep]++
				if strings.Contains(value[:last], other) {
					thousands = other
				}
			case 3:
				votes[other]++ // "1,234" or "1.234": grouping
			}
			if strings.ContainsAny(value, "   ") && thousands == "" {
				thousands = " "
			}
			if strings.ContainsAny(value, "'’") {
				thousands = "'"
			}
		}
	}
	decimal := "."
	if votes[","] > votes["."] {
		decimal = ","
	}
	if thousands == decimal {
		thousands = ""
	}
	if thousands == "" {
		thousands = map[string]string{".": ",", ",": "."}[decimal]
	}
	return decimal, thousands
}
