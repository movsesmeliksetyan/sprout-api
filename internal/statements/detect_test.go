package statements_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

func fixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata/statements", path))
	require.NoError(t, err)
	return data
}

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file      string
		format    statements.Format
		encoding  string
		delimiter rune
		header    int
		contains  string
	}{
		{"comma.csv", statements.FormatCSV, "utf-8", ',', 0, "PRET, LONDON"},
		{"semicolon.csv", statements.FormatCSV, "utf-8", ';', 0, "Buchungstag"},
		{"preamble.csv", statements.FormatCSV, "utf-8", ',', 5, "Paid out"}, // six lines, one blank
		{"utf16-bom.csv", statements.FormatCSV, "utf-16le", '\t', 0, "COMPRA MERCADONA"},
		{"windows-1252.csv", statements.FormatCSV, "windows-1252", ',', 0, "CAFÉ NOIR £"},
		{"pipe.txt", statements.FormatCSV, "utf-8", '|', 0, "TESCO"},
		{"bank.ofx", statements.FormatOFX, "utf-8", 0, 0, "<STMTTRN>"},
		{"statement.qfx", statements.FormatOFX, "utf-8", 0, 0, "OFXHEADER"},
		{"statement.pdf", statements.FormatPDF, "", 0, 0, ""},
		{"really-xlsx.csv", statements.FormatXLSX, "", 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			got, err := statements.Detect(fixture(t, "detect/"+tt.file))
			require.NoError(t, err)
			assert.Equal(t, tt.format, got.Format)
			assert.Equal(t, tt.encoding, got.Encoding)
			if tt.format == statements.FormatCSV {
				assert.Equal(t, string(tt.delimiter), string(got.Delimiter))
				assert.Equal(t, '"', got.Quote)
				assert.Equal(t, tt.header, got.HeaderRow)
			}
			assert.True(t, strings.Contains(got.Text, tt.contains), "text holds %q", tt.contains)
		})
	}
}

func TestDetect_Refuses(t *testing.T) {
	t.Parallel()
	for file, want := range map[string]error{
		"garbage.bin":   statements.ErrUnsupportedFormat,
		"document.docx": statements.ErrUnsupportedFormat,
		"prose.txt":     statements.ErrUnsupportedFormat,
		"empty.csv":     statements.ErrEmptyFile,
	} {
		_, err := statements.Detect(fixture(t, "detect/"+file))
		assert.ErrorIs(t, err, want, file)
	}
	_, err := statements.Detect(nil)
	assert.ErrorIs(t, err, statements.ErrEmptyFile)
	_, err = statements.Detect([]byte("Date;Description;Amount\n"))
	assert.ErrorIs(t, err, statements.ErrEmptyFile, "a header alone")
}

func TestDetect_UTF16WithoutBOM(t *testing.T) {
	t.Parallel()
	text := "Date,Amount\n2025-07-14,-4.50\n2025-07-13,-2.10\n"
	le := make([]byte, 0, 2*len(text))
	be := make([]byte, 0, 2*len(text))
	for _, b := range []byte(text) {
		le = append(le, b, 0)
		be = append(be, 0, b)
	}
	for name, data := range map[string][]byte{"utf-16le": le, "utf-16be": be} {
		got, err := statements.Detect(data)
		require.NoError(t, err, name)
		assert.Equal(t, name, got.Encoding)
		assert.Equal(t, text, got.Text)
	}
}
