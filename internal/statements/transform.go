package statements

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Row is one table row after transforming: a transaction, or the reason it
// could not be read.
type Row struct {
	NormalizedTransaction
	// Raw is the row's cells as read.
	Raw []string
	// Error says why the row could not be read; empty when it was.
	Error string
}

// Transform applies a spec to every row of a table (PRD §4.3 stage 4). A
// row that cannot be read is returned with its error; it never stops the
// others. Dates are midnight in loc (UTC when nil).
func Transform(table RawTable, spec MappingSpec, loc *time.Location) []Row {
	if loc == nil {
		loc = time.UTC
	}
	rows := make([]Row, len(table.Rows))
	for i, cells := range table.Rows {
		rows[i] = transformRow(i, cells, spec, loc)
	}
	return rows
}

func transformRow(index int, cells []string, spec MappingSpec, loc *time.Location) (row Row) {
	row = Row{NormalizedTransaction: NormalizedTransaction{RowIndex: index}, Raw: cells}
	defer func() {
		if r := recover(); r != nil {
			row = Row{NormalizedTransaction: NormalizedTransaction{RowIndex: index}, Raw: cells, Error: fmt.Sprintf("unreadable row: %v", r)}
		}
	}()
	cell := func(column *int) string {
		if column == nil || *column < 0 || *column >= len(cells) {
			return ""
		}
		return strings.TrimSpace(cells[*column])
	}

	// First, so that a row that cannot be read still shows what it was.
	var parts []string
	for _, column := range spec.Columns.Description {
		if text := cell(&column); text != "" {
			parts = append(parts, text)
		}
	}
	row.RawDescription = strings.Join(strings.Fields(strings.Join(parts, " ")), " ")

	date := spec.Columns.Date
	posted, err := ParseDate(cell(&date), spec.DateFormat, loc)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.PostedAt = posted

	amount, direction, err := rowAmount(cell, spec)
	if err == nil && spec.Columns.Direction != nil {
		if marked, ok := directionMarker(cell(spec.Columns.Direction)); ok {
			direction = marked
		}
	}
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.AmountMinor, row.Direction = amount, direction

	if balance := cell(spec.Columns.Balance); balance != "" {
		if minor, sign, err := ParseAmount(balance, spec.DecimalSeparator, spec.ThousandsSeparator); err == nil {
			if sign == SignNegative || sign == SignDebit {
				minor = -minor
			}
			row.BalanceAfter = &minor
		}
	}
	if currency := strings.ToUpper(cell(spec.Columns.Currency)); len(currency) == 3 && isLetters(currency) {
		row.Currency = currency
	} else if spec.CurrencyDefault != nil {
		row.Currency = strings.ToUpper(*spec.CurrencyDefault)
	}
	row.ExternalRef = cell(spec.Columns.Reference)
	if mcc := cell(spec.Columns.MCC); len(mcc) == 4 && allDigits(mcc) {
		row.MCC = mcc
	}
	return row
}

// rowAmount is a row's amount and direction: from one signed amount column
// by the spec's sign convention (a CR or DR marker wins), or from a debit
// and a credit column, of which exactly one may hold a non-zero amount.
func rowAmount(cell func(*int) string, spec MappingSpec) (int64, Direction, error) {
	parse := func(column *int) (int64, Sign, bool, error) {
		minor, sign, err := ParseAmount(cell(column), spec.DecimalSeparator, spec.ThousandsSeparator)
		if errors.Is(err, errNoAmount) {
			return 0, sign, false, nil
		}
		return minor, sign, err == nil && minor != 0, err
	}
	if spec.Columns.Amount != nil {
		minor, sign, present, err := parse(spec.Columns.Amount)
		switch {
		case err != nil:
			return 0, "", err
		case !present:
			return 0, "", fmt.Errorf("no amount")
		}
		switch sign {
		case SignDebit:
			return minor, Debit, nil
		case SignCredit:
			return minor, Credit, nil
		}
		expense := sign == SignNegative
		if spec.AmountSign == PositiveIsExpense {
			expense = !expense
		}
		if expense {
			return minor, Debit, nil
		}
		return minor, Credit, nil
	}
	debit, _, hasDebit, err := parse(spec.Columns.Debit)
	if err != nil {
		return 0, "", fmt.Errorf("debit: %w", err)
	}
	credit, _, hasCredit, err := parse(spec.Columns.Credit)
	if err != nil {
		return 0, "", fmt.Errorf("credit: %w", err)
	}
	switch {
	case hasDebit && hasCredit:
		return 0, "", fmt.Errorf("both a debit and a credit")
	case hasDebit:
		return debit, Debit, nil
	case hasCredit:
		return credit, Credit, nil
	}
	return 0, "", fmt.Errorf("no amount")
}

func isLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
