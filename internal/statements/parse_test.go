package statements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

func TestParseAmount(t *testing.T) {
	t.Parallel()
	const (
		pos = statements.SignPositive
		neg = statements.SignNegative
		dr  = statements.SignDebit
		cr  = statements.SignCredit
	)
	tests := []struct {
		cell, decimal, thousands string
		minor                    int64
		sign                     statements.Sign
	}{
		{"42.80", ".", ",", 4280, pos},
		{"-42.80", ".", ",", 4280, neg},
		{"+42.80", ".", ",", 4280, pos},
		{"42.8", ".", ",", 4280, pos},
		{"42", ".", ",", 4200, pos},
		{".5", ".", ",", 50, pos},
		{"0.01", ".", ",", 1, pos},
		{"1,234.56", ".", ",", 123456, pos},
		{"-1,234,567.89", ".", ",", 123456789, neg},
		{"1234.56", ".", ",", 123456, pos},
		{"1234.56", ".", "", 123456, pos},
		{"42,80", ",", ".", 4280, pos},
		{"-42,80", ",", ".", 4280, neg},
		{"1.234,56", ",", ".", 123456, pos},
		{"-1.234.567,89", ",", ".", 123456789, neg},
		{"2.500,00", ",", ".", 250000, pos},
		{"1 234,56", ",", " ", 123456, pos},
		{"1 234,56", ",", " ", 123456, pos},
		{"1 234,56", ",", " ", 123456, pos},
		{"1'234.56", ".", "'", 123456, pos},
		{"1’234.56", ".", "'", 123456, pos},
		{"(42.80)", ".", ",", 4280, neg},
		{"(1,234.56)", ".", ",", 123456, neg},
		{"42.80-", ".", ",", 4280, neg},
		{"42,80-", ",", ".", 4280, neg},
		{"42.80 CR", ".", ",", 4280, cr},
		{"42.80CR", ".", ",", 4280, cr},
		{"42.80 DR", ".", ",", 4280, dr},
		{"42.80 dr", ".", ",", 4280, dr},
		{"1,234.56 CR", ".", ",", 123456, cr},
		{"£42.80", ".", ",", 4280, pos},
		{"-£42.80", ".", ",", 4280, neg},
		{"£-42.80", ".", ",", 4280, neg},
		{"$1,234.56", ".", ",", 123456, pos},
		{"-$5.00", ".", ",", 500, neg},
		{"€ 42,80", ",", ".", 4280, pos},
		{"42,80 €", ",", ".", 4280, pos},
		{"-42,80 €", ",", ".", 4280, neg},
		{"42.80 EUR", ".", ",", 4280, pos},
		{"EUR 42.80", ".", ",", 4280, pos},
		{"GBP -42.80", ".", ",", 4280, neg},
		{"4 500,00 AMD", ",", " ", 450000, pos},
		{"4500 ֏", ".", ",", 450000, pos},
		{"1 234,50 руб", ",", " ", 123450, pos},
		{"−42.80", ".", ",", 4280, neg},
		{"–42.80", ".", ",", 4280, neg},
		{"  42.80  ", ".", ",", 4280, pos},
		{"42.800", ".", ",", 4280, pos},
		{"42.8000", ".", ",", 4280, pos},
		{"0", ".", ",", 0, pos},
		{"0.00", ".", ",", 0, pos},
		{"-0.00", ".", ",", 0, neg},
		{"100000000.00", ".", ",", 10000000000, pos},
		{"12", ",", ".", 1200, pos},
		{"12.345", ",", ".", 1234500, pos},
		{"-12.345", ",", ".", 1234500, neg},
		{"(£42.80)", ".", ",", 4280, neg},
		{"- 42.80", ".", ",", 4280, neg},
		{"+ 42,80", ",", ".", 4280, pos},
		{"USD 1,000", ".", ",", 100000, pos},
		{"1,000", ".", ",", 100000, pos},
		{"1,5", ",", "", 150, pos},
	}
	require.GreaterOrEqual(t, len(tests), 60)
	for _, tt := range tests {
		minor, sign, err := statements.ParseAmount(tt.cell, tt.decimal, tt.thousands)
		if assert.NoError(t, err, "%q", tt.cell) {
			assert.Equal(t, tt.minor, minor, "%q", tt.cell)
			assert.Equal(t, tt.sign, sign, "%q sign", tt.cell)
		}
	}
}

func TestParseAmount_Refuses(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ cell, decimal, thousands string }{
		{"", ".", ","},
		{"   ", ".", ","},
		{"abc", ".", ","},
		{"42.81.3", ".", ""},
		{"42.805", ".", ","},
		{"1,2,3,4", ",", ""},
		{"12x34", ".", ","},
		{"-", ".", ","},
		{"()", ".", ","},
		{"99999999999999.00", ".", ","},
		{"N/A", ".", ","},
		{"42.80 USD EUR GBP extra", ".", ","},
	} {
		_, _, err := statements.ParseAmount(tt.cell, tt.decimal, tt.thousands)
		assert.Error(t, err, "%q", tt.cell)
	}
}

func FuzzParseAmount(f *testing.F) {
	for _, seed := range []string{"42.80", "(1,234.56)", "42,80-", "£-1", "1 234,5 CR", "−", "--1", "((1))"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cell string) {
		minor, _, err := statements.ParseAmount(cell, ".", ",")
		if err == nil && (minor < 0 || minor > 10_000_000_000_000) {
			t.Fatalf("%q gave %d", cell, minor)
		}
	})
}

func TestParseDate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cell, format string
		want         string
	}{
		{"14/07/2025", "DD/MM/YYYY", "2025-07-14"},
		{"14.07.2025", "DD.MM.YYYY", "2025-07-14"},
		{"14-07-2025", "DD-MM-YYYY", "2025-07-14"},
		{"4/7/2025", "DD/MM/YYYY", "2025-07-04"},
		{"07/14/2025", "MM/DD/YYYY", "2025-07-14"},
		{"7/4/2025", "MM/DD/YYYY", "2025-07-04"},
		{"2025-07-14", "YYYY-MM-DD", "2025-07-14"},
		{"2025/07/14", "YYYY/MM/DD", "2025-07-14"},
		{"20250714", "YYYYMMDD", "2025-07-14"},
		{"14072025", "DDMMYYYY", "2025-07-14"},
		{"14/07/25", "DD/MM/YY", "2025-07-14"},
		{"07/14/25", "MM/DD/YY", "2025-07-14"},
		{"14 Jul 2025", "DD MMM YYYY", "2025-07-14"},
		{"14-JUL-2025", "DD-MMM-YYYY", "2025-07-14"},
		{"14 July 2025", "DD MMM YYYY", "2025-07-14"},
		{"Jul 14 2025", "MMM DD YYYY", "2025-07-14"},
		{"14 Jul 25", "DD MMM YY", "2025-07-14"},
		{"14. März 2025", "DD. MMM YYYY", "2025-03-14"},
		{"14 maerz 2025", "DD MMM YYYY", "2025-03-14"},
		{"14 févr. 2025", "DD MMM YYYY", "2025-02-14"},
		{"14 juil. 2025", "DD MMM YYYY", "2025-07-14"},
		{"14 ene 2025", "DD MMM YYYY", "2025-01-14"},
		{"14 dic 2025", "DD MMM YYYY", "2025-12-14"},
		{"14 mei 2025", "DD MMM YYYY", "2025-05-14"},
		{"14 out 2025", "DD MMM YYYY", "2025-10-14"},
		{"14 października 2025", "DD MMM YYYY", "2025-10-14"},
		{"14 июля 2025", "DD MMM YYYY", "2025-07-14"},
		{"14 май 2025", "DD MMM YYYY", "2025-05-14"},
		{"14 հուլիսի 2025", "DD MMM YYYY", "2025-07-14"},
		{"14/07/2025 08:12", "DD/MM/YYYY", "2025-07-14"},
		{"2025-07-14T08:12:00", "YYYY-MM-DD", "2025-07-14"},
		{"29/02/2024", "DD/MM/YYYY", "2024-02-29"},
		{"  14/07/2025 ", "DD/MM/YYYY", "2025-07-14"},
		{"14.7.2025", "dd.mm.yyyy", "2025-07-14"},
	}
	require.GreaterOrEqual(t, len(tests), 30)
	loc := london
	for _, tt := range tests {
		got, err := statements.ParseDate(tt.cell, tt.format, loc)
		if assert.NoError(t, err, "%q %s", tt.cell, tt.format) {
			assert.Equal(t, tt.want, got.Format(time.DateOnly), "%q %s", tt.cell, tt.format)
			assert.Equal(t, loc, got.Location())
			assert.Zero(t, got.Hour(), "midnight in the user's timezone")
		}
	}
}

func TestParseDate_Refuses(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ cell, format string }{
		{"", "DD/MM/YYYY"},
		{"31/02/2025", "DD/MM/YYYY"},
		{"14/13/2025", "DD/MM/YYYY"},
		{"07/14/2025", "DD/MM/YYYY"},
		{"14/07", "DD/MM/YYYY"},
		{"14 Foo 2025", "DD MMM YYYY"},
		{"14/07/2025", "QQ/MM/YYYY"},
		{"114/07/2025", "DD/MM/YYYY"},
		{"14/07/225", "DD/MM/YYYY"},
		{"Total", "DD/MM/YYYY"},
	} {
		_, err := statements.ParseDate(tt.cell, tt.format, london)
		assert.Error(t, err, "%q %s", tt.cell, tt.format)
	}
	assert.True(t, statements.ValidDateFormat("DD.MM.YYYY"))
	assert.True(t, statements.ValidDateFormat("YYYYMMDD"))
	assert.False(t, statements.ValidDateFormat("DD.MM"))
	assert.False(t, statements.ValidDateFormat("DD.DD.YYYY"))
	assert.False(t, statements.ValidDateFormat("DD#MM#YYYY"))
}
