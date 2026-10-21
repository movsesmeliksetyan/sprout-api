package statements

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// RawTable is a statement's table as read, before anything is interpreted
// (PRD §4.3 stage 2).
type RawTable struct {
	// Headers are the column labels, one per column.
	Headers []string `json:"headers"`
	// Rows are the data rows, each exactly as wide as Headers.
	Rows [][]string `json:"rows"`
	// Preamble are the lines before the header, such as the account
	// holder and the period.
	Preamble []string `json:"preamble"`
	// Footer are the rows after the data, such as totals and closing
	// balances.
	Footer [][]string `json:"footer"`
}

// ErrNoRows is a table with a header and nothing under it.
var ErrNoRows = errors.New("statements: the table has no rows")

// ExtractCSV reads delimited text into a table, tolerantly: stray quotes
// are kept as text, quoted cells may span lines, rows of any length are
// padded to the table's width, blank rows are dropped, and a record that
// cannot be parsed is skipped rather than ending the file.
func ExtractCSV(d Detection) (RawTable, error) {
	reader := csv.NewReader(strings.NewReader(d.Text))
	reader.Comma = d.Delimiter
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	var records [][]string
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		var parseErr *csv.ParseError
		if errors.As(err, &parseErr) {
			continue
		}
		if err != nil {
			return RawTable{}, err
		}
		records = append(records, record)
	}
	if d.HeaderRow >= len(records) {
		return RawTable{}, ErrNoRows
	}
	return tableOf(records, d.HeaderRow, string(d.Delimiter)), nil
}

// tableOf splits records at the header: what comes before is the
// preamble, joined back into lines, and the rest is the header and data,
// with trailing rows that are not data moved to the footer.
func tableOf(records [][]string, header int, delimiter string) RawTable {
	table := RawTable{Preamble: []string{}, Footer: [][]string{}, Rows: [][]string{}}
	for _, record := range records[:header] {
		if line := strings.TrimSpace(strings.Join(nonEmptyTail(record), delimiter)); line != "" {
			table.Preamble = append(table.Preamble, line)
		}
	}
	width := len(nonEmptyTail(records[header]))
	var rows [][]string
	for _, record := range records[header+1:] {
		record = trimCells(record)
		if len(nonEmptyTail(record)) == 0 {
			continue
		}
		width = max(width, len(nonEmptyTail(record)))
		rows = append(rows, record)
	}
	table.Headers = pad(trimCells(records[header]), width)
	for i := range rows {
		rows[i] = pad(rows[i], width)
	}
	data := len(rows) - footerRows(rows)
	table.Rows = append(table.Rows, rows[:data]...)
	table.Footer = append(table.Footer, rows[data:]...)
	return table
}

// footerRows is how many rows at the end are not data: rows without a
// date in the date column (the first whose first row and at least half of
// all rows hold a date), or, in a table without one, rows less than half
// full.
func footerRows(rows [][]string) int {
	if len(rows) < 2 {
		return 0
	}
	dateColumn := -1
	for column := range rows[0] {
		dates := 0
		for _, row := range rows {
			if looksDate(strings.TrimSpace(row[column])) {
				dates++
			}
		}
		if looksDate(strings.TrimSpace(rows[0][column])) && dates*2 >= len(rows) {
			dateColumn = column
			break
		}
	}
	n := 0
	for i := len(rows) - 1; i > 0; i-- {
		row := rows[i]
		var footer bool
		if dateColumn >= 0 {
			footer = !looksDate(strings.TrimSpace(row[dateColumn]))
		} else {
			footer = len(nonEmptyTail(row))*2 < len(row)
		}
		if !footer {
			break
		}
		n++
	}
	return n
}

func trimCells(record []string) []string {
	out := make([]string, len(record))
	for i, cell := range record {
		out[i] = strings.TrimSpace(cell)
	}
	return out
}

// pad makes a record width cells long, adding empty cells or dropping
// empty ones from its end.
func pad(record []string, width int) []string {
	record = nonEmptyTail(record)
	if len(record) >= width {
		return record
	}
	return append(record, make([]string, width-len(record))...)
}
