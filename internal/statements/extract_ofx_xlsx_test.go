package statements_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

var london = mustLoad("Europe/London")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func TestExtractOFX_Golden(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"bank-v1.ofx", "creditcard-v2.qfx"} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("../../testdata/statements/ofx", file)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			detected, err := statements.Detect(data)
			require.NoError(t, err)
			require.Equal(t, statements.FormatOFX, detected.Format)
			txs, err := statements.ExtractOFX(detected, london)
			require.NoError(t, err)
			require.Len(t, txs, 4)
			golden(t, strings.TrimSuffix(path, filepath.Ext(path))+".golden.json", txs)
		})
	}
}

func TestExtractOFX_Unreadable(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"bad amount":  "<STMTTRN><DTPOSTED>20250714<TRNAMT>12.3.4<FITID>1</STMTTRN>",
		"bad date":    "<STMTTRN><DTPOSTED>2025-07-14<TRNAMT>-1.00<FITID>1</STMTTRN>",
		"zero amount": "<STMTTRN><DTPOSTED>20250714<TRNAMT>0.00<FITID>1</STMTTRN>",
		"not closed":  "<STMTTRN><DTPOSTED>20250714<TRNAMT>-1.00<FITID>1",
	} {
		_, err := statements.ExtractOFX(statements.Detection{Text: "OFXHEADER:100\n<OFX>" + body + "</OFX>"}, london)
		assert.ErrorIs(t, err, statements.ErrUnreadable, name)
	}
}

// xlsxFixtures build the spreadsheet fixtures, written with -update.
var xlsxFixtures = map[string]func(f *excelize.File) error{
	"bank-dates.xlsx": func(f *excelize.File) error {
		date, err := f.NewStyle(&excelize.Style{NumFmt: 14})
		if err != nil {
			return err
		}
		rows := [][]any{
			{"Statement for account ****1234"},
			{},
			{"Date", "Description", "Amount", "Balance"},
			{45852, "TESCO STORES 3297", -42.8, 1203.11},
			{45851, "PRET A MANGER", -4.5, 1245.91},
			{45850, "SALARY ACME", 2500, 1250.41},
		}
		for i, row := range rows {
			if err := f.SetSheetRow("Sheet1", "A"+strconv.Itoa(i+1), &row); err != nil {
				return err
			}
		}
		return f.SetCellStyle("Sheet1", "A4", "A6", date)
	},
	"merged-header.xlsx": func(f *excelize.File) error {
		if _, err := f.NewSheet("Empty"); err != nil {
			return err
		}
		if err := f.SetSheetName("Sheet1", "Umsätze"); err != nil {
			return err
		}
		f.SetActiveSheet(1)
		date, err := f.NewStyle(&excelize.Style{CustomNumFmt: ptr("dd.mm.yyyy")})
		if err != nil {
			return err
		}
		rows := [][]any{
			{"Datum", "Text", "Betrag", ""},
			{"", "", "Soll", "Haben"},
			{45852, "KARTENZAHLUNG REWE", 42.8, nil},
			{45850, "GEHALT ACME GMBH", nil, 2500},
		}
		for i, row := range rows {
			if err := f.SetSheetRow("Umsätze", "A"+strconv.Itoa(i+1), &row); err != nil {
				return err
			}
		}
		if err := f.MergeCell("Umsätze", "C1", "D1"); err != nil {
			return err
		}
		return f.SetCellStyle("Umsätze", "A3", "A4", date)
	},
}

func ptr[T any](v T) *T { return &v }

func TestExtractXLSX_Golden(t *testing.T) {
	t.Parallel()
	for file, build := range xlsxFixtures {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("../../testdata/statements/xlsx", file)
			if *update {
				f := excelize.NewFile()
				require.NoError(t, build(f))
				require.NoError(t, f.SaveAs(path))
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			detected, err := statements.Detect(data)
			require.NoError(t, err)
			require.Equal(t, statements.FormatXLSX, detected.Format)
			table, err := statements.ExtractXLSX(data)
			require.NoError(t, err)
			golden(t, strings.TrimSuffix(path, ".xlsx")+".golden.json", table)
		})
	}
}

func TestExtractXLSX_Refuses(t *testing.T) {
	t.Parallel()
	_, err := statements.ExtractXLSX([]byte("PK\x03\x04 not really"))
	assert.ErrorIs(t, err, statements.ErrUnreadable)

	f := excelize.NewFile()
	buf, err := f.WriteToBuffer()
	require.NoError(t, err)
	_, err = statements.ExtractXLSX(buf.Bytes())
	assert.ErrorIs(t, err, statements.ErrEmptyFile)
}
