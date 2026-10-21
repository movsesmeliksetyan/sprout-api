package statements

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Format is a statement's container.
type Format string

// The containers Sprout reads.
const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
	FormatOFX  Format = "ofx"
	FormatPDF  Format = "pdf"
)

// Detection errors; the import fails with the matching code.
var (
	ErrEmptyFile         = errors.New("statements: the file is empty")
	ErrUnsupportedFormat = errors.New("statements: not a statement format Sprout reads")
)

// Detection is what stage 1 found out about a file.
type Detection struct {
	Format Format
	// Encoding the text was in: utf-8, utf-16le, utf-16be or windows-1252.
	// Empty for binary containers.
	Encoding string
	// Text is the file as UTF-8, for CSV and OFX.
	Text string
	// Delimiter, Quote and HeaderRow describe delimited text: the cell
	// separator, the quote character, and the index of the header among
	// the file's records (the records before it are a preamble; blank
	// lines are not records).
	Delimiter rune
	Quote     rune
	HeaderRow int
}

// sniffBytes is how much of a file sniffing looks at.
const sniffBytes = 4096

// delimiters are the cell separators tried, in order of preference on a
// tie.
var delimiters = []rune{',', ';', '\t', '|'}

// Detect sniffs a file by its content, never its name (PRD §4.3 stage 1).
func Detect(data []byte) (Detection, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Detection{}, ErrEmptyFile
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return Detection{Format: FormatPDF}, nil
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		if bytes.Contains(data, []byte("xl/")) {
			return Detection{Format: FormatXLSX}, nil
		}
		return Detection{}, ErrUnsupportedFormat
	}

	text, encoding, ok := decode(data)
	if !ok {
		return Detection{}, ErrUnsupportedFormat
	}
	if strings.TrimSpace(text) == "" {
		return Detection{}, ErrEmptyFile
	}
	head := strings.ToUpper(text[:min(len(text), sniffBytes)])
	if strings.Contains(head, "OFXHEADER") || strings.Contains(head, "<OFX>") {
		return Detection{Format: FormatOFX, Encoding: encoding, Text: text}, nil
	}

	delimiter, records, ok := bestDelimiter(text)
	if !ok {
		if headerOnly(text) {
			return Detection{}, ErrEmptyFile
		}
		return Detection{}, ErrUnsupportedFormat
	}
	return Detection{
		Format:    FormatCSV,
		Encoding:  encoding,
		Text:      text,
		Delimiter: delimiter,
		Quote:     '"',
		HeaderRow: headerRow(records),
	}, nil
}

// decode turns a text file into UTF-8: by its byte-order mark, as UTF-8
// when it is valid, as UTF-16 when every other byte is zero, else as
// Windows-1252. A file with many control characters is not text.
func decode(data []byte) (text, encoding string, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		text, encoding = string(data[3:]), "utf-8"
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		text, encoding = decodeUTF16(data[2:], false), "utf-16le"
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		text, encoding = decodeUTF16(data[2:], true), "utf-16be"
	case looksUTF16(data, false):
		text, encoding = decodeUTF16(data, false), "utf-16le"
	case looksUTF16(data, true):
		text, encoding = decodeUTF16(data, true), "utf-16be"
	case utf8.Valid(data):
		text, encoding = string(data), "utf-8"
	default:
		decoded, err := charmap.Windows1252.NewDecoder().Bytes(data)
		if err != nil {
			return "", "", false
		}
		text, encoding = string(decoded), "windows-1252"
	}
	return text, encoding, isText(text)
}

// looksUTF16 reports whether most of the high (or low) bytes of the first
// characters are zero, as in UTF-16 text in a Latin script.
func looksUTF16(data []byte, bigEndian bool) bool {
	n := min(len(data), sniffBytes) &^ 1
	if n < 4 {
		return false
	}
	zeros := 0
	for i := 0; i < n; i += 2 {
		b := data[i+1]
		if bigEndian {
			b = data[i]
		}
		if b == 0 {
			zeros++
		}
	}
	return zeros*10 >= (n/2)*9
}

func decodeUTF16(data []byte, bigEndian bool) string {
	units := make([]uint16, len(data)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		} else {
			units[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
		}
	}
	return string(utf16.Decode(units))
}

// isText reports whether at most 1 % of the start is control characters
// other than white space.
func isText(text string) bool {
	head := text[:min(len(text), sniffBytes)]
	total, control := 0, 0
	for _, r := range head {
		total++
		if r == utf8.RuneError || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
			control++
		}
	}
	return total > 0 && control*100 <= total
}

// sampleRecords is how many records delimiter and header detection read.
const sampleRecords = 60

// bestDelimiter is the separator under which the most records share one
// cell count of at least two, the wider count on a tie; the records are
// the file read with it.
func bestDelimiter(text string) (rune, [][]string, bool) {
	var best rune
	var bestRecords [][]string
	bestCount, bestWidth := 0, 0
	for _, delimiter := range delimiters {
		records := readRecords(text, delimiter, sampleRecords)
		width, count := modalWidth(records)
		if count < 2 {
			continue // a table has a header and a row at least
		}
		if count > bestCount || (count == bestCount && width > bestWidth) {
			best, bestRecords, bestCount, bestWidth = delimiter, records, count, width
		}
	}
	return best, bestRecords, bestCount > 0
}

// headerOnly reports whether text is one record of several cells: a
// table's header with nothing under it.
func headerOnly(text string) bool {
	for _, delimiter := range delimiters {
		records := readRecords(text, delimiter, 2)
		if len(records) == 1 && len(nonEmptyTail(records[0])) >= 2 {
			return true
		}
	}
	return false
}

// readRecords reads up to limit records of delimited text, tolerantly:
// stray quotes are kept, rows may differ in length, and a broken record
// ends the sample rather than the file.
func readRecords(text string, delimiter rune, limit int) [][]string {
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = delimiter
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	var records [][]string
	for len(records) < limit {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
		records = append(records, record)
	}
	return records
}

// modalWidth is the most common number of cells among records of at least
// two, and how many records have it: a table's width, whatever lines of
// one cell come before it.
func modalWidth(records [][]string) (width, count int) {
	counts := map[int]int{}
	for _, record := range records {
		// The record's own width, empty cells at the end included: a
		// debit/credit layout leaves the last column empty on half its rows.
		if len(nonEmptyTail(record)) > 1 {
			counts[len(record)]++
		}
	}
	for w, c := range counts {
		if c > count || (c == count && w > width) {
			width, count = w, c
		}
	}
	return width, count
}

// nonEmptyTail is the record without the empty cells at its end.
func nonEmptyTail(record []string) []string {
	end := len(record)
	for end > 0 && strings.TrimSpace(record[end-1]) == "" {
		end--
	}
	return record[:end]
}

// headerRow is the index of the first record that looks like column labels
// over data: as wide as the table, mostly text that is neither a number nor
// a date, followed by a record holding a number or a date. Without one the
// first record of the table's width is the header.
func headerRow(records [][]string) int {
	width, _ := modalWidth(records)
	first := -1
	for i, record := range records {
		if len(nonEmptyTail(record)) < max(2, width-1) {
			continue
		}
		if first < 0 {
			first = i
		}
		if !labels(record) {
			continue
		}
		if i+1 < len(records) && holdsData(records[i+1]) {
			return i
		}
	}
	return max(first, 0)
}

// labels reports whether at least 60 % of a record's non-empty cells read
// as labels rather than values.
func labels(record []string) bool {
	cells, text := 0, 0
	for _, cell := range record {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		cells++
		if !looksNumeric(cell) && !looksDate(cell) {
			text++
		}
	}
	return cells > 0 && text*10 >= cells*6
}

// holdsData reports whether a record has a number or a date in it.
func holdsData(record []string) bool {
	for _, cell := range record {
		cell = strings.TrimSpace(cell)
		if cell != "" && (looksNumeric(cell) || looksDate(cell)) {
			return true
		}
	}
	return false
}

// looksNumeric reports whether a cell is an amount: digits with at most
// separators, a sign, parentheses, a currency symbol or code around them.
func looksNumeric(cell string) bool {
	digits, other := 0, 0
	for _, r := range cell {
		switch {
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune("+-.,'()  ", r), unicode.Is(unicode.Sc, r):
		default:
			other++
		}
	}
	return digits > 0 && other <= 3
}

// looksDate reports whether a cell is a date: digits of a day and month
// (one or two) and a year (two or four) joined by one of ./- or spaces,
// around at most one word of up to nine letters (a month name: "14 Jul
// 2025", "14. März 2025"); a day and month alone with a month name or a
// slash ("14 Jul", "14/07"); or eight digits that read as YYYYMMDD. An
// amount such as -42.80 or 1203.11 is not a date.
func looksDate(cell string) bool {
	cell = withoutTime(strings.TrimSpace(cell))
	if len(cell) == 8 && allDigits(cell) {
		month, day := (cell[4]-'0')*10+(cell[5]-'0'), (cell[6]-'0')*10+(cell[7]-'0')
		return (cell[:2] == "19" || cell[:2] == "20") && month >= 1 && month <= 12 && day >= 1 && day <= 31
	}
	var groups []int
	words, letters := 0, 0
	slash := false
	inDigits, inWord := false, false
	for i, r := range cell {
		switch {
		case unicode.IsDigit(r):
			if !inDigits {
				groups = append(groups, 0)
			}
			groups[len(groups)-1]++
			inDigits, inWord = true, false
			continue
		case unicode.IsLetter(r):
			if !inWord {
				words++
			}
			letters++
			inDigits, inWord = false, true
			continue
		case i == 0:
			return false // a sign or a bracket: an amount
		case r == '/':
			slash = true
		case !strings.ContainsRune(".- ", r):
			return false
		}
		inDigits, inWord = false, false
	}
	if words > 1 || letters > 9 {
		return false
	}
	day := func(n int) bool { return n == 1 || n == 2 }
	year := func(n int) bool { return n == 2 || n == 4 }
	switch {
	case len(groups) == 3 && words == 0:
		return day(groups[0]) && day(groups[1]) && year(groups[2]) || groups[0] == 4 && day(groups[1]) && day(groups[2])
	case len(groups) == 2 && words == 1:
		return day(groups[0]) && year(groups[1]) // 14 Jul 2025, Jul 14 2025
	case len(groups) == 2 && slash:
		return day(groups[0]) && day(groups[1]) // 14/07
	}
	return false
}

// withoutTime drops a time after a date: "14/07/2025 08:12" and
// "2025-07-14T08:12:00" are dates.
func withoutTime(cell string) string {
	colon := strings.IndexByte(cell, ':')
	if colon < 0 {
		return cell
	}
	cut := strings.LastIndexAny(cell[:colon], " T")
	if cut <= 0 {
		return cell
	}
	return strings.TrimSpace(cell[:cut])
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
