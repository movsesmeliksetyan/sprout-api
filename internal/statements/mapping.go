package statements

import (
	"errors"
	"fmt"
	"strings"
)

// AmountSign says which sign of a single amount column is money out.
type AmountSign string

// The sign conventions (contract §2.10).
const (
	NegativeIsExpense AmountSign = "negative_is_expense"
	PositiveIsExpense AmountSign = "positive_is_expense"
)

// MappingSpec is how to read one statement layout (PRD §4.2), serialised
// into templates and imports. Column fields are indexes into the table's
// columns; nil is absent.
type MappingSpec struct {
	// HeaderRow and SkipFooterRows place the table in the file; extraction
	// has already cut the table out, so transforming does not use them.
	HeaderRow          int            `json:"header_row"`
	SkipFooterRows     int            `json:"skip_footer_rows"`
	Columns            MappingColumns `json:"columns"`
	DateFormat         string         `json:"date_format"`
	DecimalSeparator   string         `json:"decimal_separator"`
	ThousandsSeparator string         `json:"thousands_separator"`
	AmountSign         AmountSign     `json:"amount_sign"`
	CurrencyDefault    *string        `json:"currency_default"`
}

// MappingColumns are the roles of a layout's columns.
type MappingColumns struct {
	Date        int   `json:"date"`
	Description []int `json:"description"`
	Amount      *int  `json:"amount"`
	Debit       *int  `json:"debit"`
	Credit      *int  `json:"credit"`
	Balance     *int  `json:"balance"`
	Currency    *int  `json:"currency"`
	Reference   *int  `json:"reference"`
	MCC         *int  `json:"mcc"`
}

// minParseRate is the share of sample rows, in percent, a valid spec must
// parse.
const minParseRate = 95

// ErrInvalidSpec is a mapping that cannot read the table.
var ErrInvalidSpec = errors.New("statements: the mapping does not fit the statement")

// Validate checks a spec's shape against a table width (PRD §4.2): one date
// column, at least one description column, either an amount column or both
// debit and credit, every column inside the table and used once, a date
// format of known tokens, and separators that can be told apart.
func (s MappingSpec) Validate(width int) error {
	var problems []string
	used := map[int]string{}
	use := func(role string, column *int) {
		if column == nil {
			return
		}
		switch {
		case *column < 0 || *column >= width:
			problems = append(problems, fmt.Sprintf("%s column %d is not in the table", role, *column))
		case used[*column] != "":
			problems = append(problems, fmt.Sprintf("column %d is both %s and %s", *column, used[*column], role))
		default:
			used[*column] = role
		}
	}
	date := s.Columns.Date
	use("date", &date)
	if len(s.Columns.Description) == 0 {
		problems = append(problems, "no description column")
	}
	for _, column := range s.Columns.Description {
		use("description", &column)
	}
	switch {
	case s.Columns.Amount != nil && (s.Columns.Debit != nil || s.Columns.Credit != nil):
		problems = append(problems, "both an amount column and debit/credit columns")
	case s.Columns.Amount == nil && (s.Columns.Debit == nil || s.Columns.Credit == nil):
		problems = append(problems, "neither an amount column nor both debit and credit columns")
	}
	use("amount", s.Columns.Amount)
	use("debit", s.Columns.Debit)
	use("credit", s.Columns.Credit)
	use("balance", s.Columns.Balance)
	use("currency", s.Columns.Currency)
	use("reference", s.Columns.Reference)
	use("mcc", s.Columns.MCC)
	if !ValidDateFormat(s.DateFormat) {
		problems = append(problems, fmt.Sprintf("date format %q is not valid", s.DateFormat))
	}
	if s.DecimalSeparator != "." && s.DecimalSeparator != "," {
		problems = append(problems, fmt.Sprintf("decimal separator %q is not . or ,", s.DecimalSeparator))
	}
	switch s.ThousandsSeparator {
	case "", ".", ",", " ", "'":
		if s.ThousandsSeparator == s.DecimalSeparator {
			problems = append(problems, "the thousands and decimal separators are the same")
		}
	default:
		problems = append(problems, fmt.Sprintf("thousands separator %q is not known", s.ThousandsSeparator))
	}
	if s.Columns.Amount != nil && s.AmountSign != NegativeIsExpense && s.AmountSign != PositiveIsExpense {
		problems = append(problems, fmt.Sprintf("amount sign %q is not known", s.AmountSign))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidSpec, strings.Join(problems, "; "))
	}
	return nil
}

// ParseRate is the share of rows, in percent, that a spec reads without an
// error.
func ParseRate(rows []Row) int {
	if len(rows) == 0 {
		return 0
	}
	ok := 0
	for _, row := range rows {
		if row.Error == "" {
			ok++
		}
	}
	return ok * 100 / len(rows)
}

// Check validates a spec against a table: its shape, and that it reads at
// least 95 % of the sample rows (the first samples rows, or all).
func (s MappingSpec) Check(table RawTable, samples int) error {
	if err := s.Validate(len(table.Headers)); err != nil {
		return err
	}
	rows := table.Rows
	if samples > 0 && len(rows) > samples {
		rows = rows[:samples]
	}
	read := Transform(RawTable{Headers: table.Headers, Rows: rows}, s, nil)
	if rate := ParseRate(read); rate < minParseRate {
		return fmt.Errorf("%w: it reads %d %% of the rows, at least %d %% are needed", ErrInvalidSpec, rate, minParseRate)
	}
	return nil
}
