package statements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

func col(i int) *int { return &i }

// signedSpec reads Date, Description, Amount, Balance with a signed amount.
func signedSpec() statements.MappingSpec {
	return statements.MappingSpec{
		Columns: statements.MappingColumns{
			Date: 0, Description: []int{1}, Amount: col(2), Balance: col(3),
		},
		DateFormat: "DD/MM/YYYY", DecimalSeparator: ".", ThousandsSeparator: ",", AmountSign: statements.NegativeIsExpense,
	}
}

func TestTransform_SignedAmount(t *testing.T) {
	t.Parallel()
	table := statements.RawTable{
		Headers: []string{"Date", "Description", "Amount", "Balance"},
		Rows: [][]string{
			{"14/07/2025", "TESCO   STORES", "-42.80", "1,203.11"},
			{"13/07/2025", "REFUND", "4.50", "-12.00"},
			{"12/07/2025", "MARKED", "10.00 DR", ""},
			{"Total", "", "-38.30", ""},
			{"11/07/2025", "NO AMOUNT", "", ""},
			{"10/07/2025", "ZERO", "0.00", ""},
			{"09/07/2025", "BAD", "12x", ""},
		},
	}
	rows := statements.Transform(table, signedSpec(), london)
	require.Len(t, rows, 7)

	assert.Empty(t, rows[0].Error)
	assert.Equal(t, time.Date(2025, 7, 14, 0, 0, 0, 0, london), rows[0].PostedAt)
	assert.Equal(t, int64(4280), rows[0].AmountMinor)
	assert.Equal(t, statements.Debit, rows[0].Direction)
	assert.Equal(t, "TESCO STORES", rows[0].RawDescription, "white space collapsed")
	require.NotNil(t, rows[0].BalanceAfter)
	assert.Equal(t, int64(120311), *rows[0].BalanceAfter)

	assert.Equal(t, statements.Credit, rows[1].Direction)
	assert.Equal(t, int64(-1200), *rows[1].BalanceAfter)
	assert.Equal(t, statements.Debit, rows[2].Direction, "DR wins over the sign")
	for _, i := range []int{3, 4, 5, 6} {
		assert.NotEmpty(t, rows[i].Error, "row %d", i)
		assert.Equal(t, i, rows[i].RowIndex)
		assert.Equal(t, table.Rows[i], rows[i].Raw)
	}
	assert.Equal(t, 42, statements.ParseRate(rows), "3 of 7")

	positive := signedSpec()
	positive.AmountSign = statements.PositiveIsExpense
	flipped := statements.Transform(table, positive, london)
	assert.Equal(t, statements.Credit, flipped[0].Direction)
	assert.Equal(t, statements.Debit, flipped[1].Direction)
}

func TestTransform_DebitCreditColumns(t *testing.T) {
	t.Parallel()
	spec := statements.MappingSpec{
		Columns: statements.MappingColumns{
			Date: 0, Description: []int{1, 2}, Debit: col(3), Credit: col(4), Currency: col(5), Reference: col(6), MCC: col(7),
		},
		DateFormat: "DD.MM.YYYY", DecimalSeparator: ",", ThousandsSeparator: ".", CurrencyDefault: ptr("eur"),
	}
	table := statements.RawTable{
		Headers: []string{"Datum", "Text", "Zusatz", "Soll", "Haben", "Währung", "Ref", "MCC"},
		Rows: [][]string{
			{"14.07.2025", "KARTENZAHLUNG", "REWE", "42,80", "", "EUR", "R1", "5411"},
			{"13.07.2025", "GEHALT", "", "", "2.500,00", "", "R2", ""},
			{"12.07.2025", "SIGNED DEBIT", "", "-9,99", "", "usd", "", "54"},
			{"11.07.2025", "BOTH", "", "1,00", "1,00", "", "", ""},
			{"10.07.2025", "NEITHER", "", "", "", "", "", ""},
			{"09.07.2025", "ZERO DEBIT", "", "0,00", "3,00", "", "", ""},
		},
	}
	rows := statements.Transform(table, spec, london)
	assert.Equal(t, statements.NormalizedTransaction{
		RowIndex: 0, PostedAt: time.Date(2025, 7, 14, 0, 0, 0, 0, london), AmountMinor: 4280, Direction: statements.Debit,
		Currency: "EUR", RawDescription: "KARTENZAHLUNG REWE", ExternalRef: "R1", MCC: "5411",
	}, rows[0].NormalizedTransaction)
	assert.Equal(t, statements.Credit, rows[1].Direction)
	assert.Equal(t, int64(250000), rows[1].AmountMinor)
	assert.Equal(t, "EUR", rows[1].Currency, "the default")
	assert.Equal(t, statements.Debit, rows[2].Direction)
	assert.Equal(t, int64(999), rows[2].AmountMinor, "a minus in the debit column changes nothing")
	assert.Equal(t, "USD", rows[2].Currency)
	assert.Empty(t, rows[2].MCC, "not four digits")
	assert.Contains(t, rows[3].Error, "both")
	assert.Contains(t, rows[4].Error, "no amount")
	assert.Empty(t, rows[5].Error)
	assert.Equal(t, statements.Credit, rows[5].Direction, "a zero debit is no debit")
}

func TestTransform_NeverPanics(t *testing.T) {
	t.Parallel()
	spec := signedSpec()
	spec.Columns.Description = []int{1, 9}
	table := statements.RawTable{Headers: []string{"a", "b", "c", "d"}, Rows: [][]string{{}, {"14/07/2025"}, {"14/07/2025", "X", "-1"}}}
	rows := statements.Transform(table, spec, nil)
	require.Len(t, rows, 3)
	assert.NotEmpty(t, rows[0].Error)
	assert.NotEmpty(t, rows[1].Error)
	assert.Empty(t, rows[2].Error)
	assert.Equal(t, time.UTC, rows[2].PostedAt.Location())
}

func TestMappingSpec_Validate(t *testing.T) {
	t.Parallel()
	require.NoError(t, signedSpec().Validate(4))
	for name, change := range map[string]func(*statements.MappingSpec){
		"no description":           func(s *statements.MappingSpec) { s.Columns.Description = nil },
		"no amount":                func(s *statements.MappingSpec) { s.Columns.Amount = nil },
		"debit without credit":     func(s *statements.MappingSpec) { s.Columns.Amount, s.Columns.Debit = nil, col(2) },
		"amount and debit/credit":  func(s *statements.MappingSpec) { s.Columns.Debit, s.Columns.Credit = col(5), col(6) },
		"column outside the table": func(s *statements.MappingSpec) { s.Columns.Balance = col(9) },
		"column used twice":        func(s *statements.MappingSpec) { s.Columns.Balance = col(2) },
		"date column outside":      func(s *statements.MappingSpec) { s.Columns.Date = -1 },
		"bad date format":          func(s *statements.MappingSpec) { s.DateFormat = "DD/MM" },
		"bad decimal":              func(s *statements.MappingSpec) { s.DecimalSeparator = ";" },
		"same separators":          func(s *statements.MappingSpec) { s.ThousandsSeparator = "." },
		"unknown thousands":        func(s *statements.MappingSpec) { s.ThousandsSeparator = "_" },
		"unknown sign":             func(s *statements.MappingSpec) { s.AmountSign = "sometimes" },
	} {
		spec := signedSpec()
		change(&spec)
		assert.ErrorIs(t, spec.Validate(4), statements.ErrInvalidSpec, name)
	}
}

func TestMappingSpec_Check(t *testing.T) {
	t.Parallel()
	rows := make([][]string, 0, 40)
	for range 39 {
		rows = append(rows, []string{"14/07/2025", "TESCO", "-1.00", ""})
	}
	rows = append(rows, []string{"bad", "TESCO", "-1.00", ""})
	table := statements.RawTable{Headers: []string{"Date", "Description", "Amount", "Balance"}, Rows: rows}
	assert.NoError(t, signedSpec().Check(table, 0), "39 of 40 is 97 %")

	wrong := signedSpec()
	wrong.DateFormat = "MM/DD/YYYY"
	assert.ErrorIs(t, wrong.Check(table, 10), statements.ErrInvalidSpec, "the wrong date order reads nothing")
}
