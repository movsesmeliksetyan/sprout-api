package statements

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

const (
	// MaxPDFPages is the longest statement read (PRD §4.5).
	MaxPDFPages = 40
	// pdfRowTolerance is how far, in percent, the rows the model returns
	// may stray from the lines that start with a date.
	pdfRowTolerance = 10
	// pdfRowSlack is how many rows apart they may be whatever the
	// percentage: a short statement's dated lines may include a balance
	// brought forward.
	pdfRowSlack = 1
	// pdfTokensPerLine bounds a page's answer.
	pdfTokensPerLine = 40
)

// ErrTooManyRows is a statement longer than Sprout reads.
var ErrTooManyRows = errors.New("statements: the statement is too long")

// pdfSchema is a page's transaction table as the model reads it.
var pdfSchema = llm.MustSchema("statement_page_table", []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["headers", "rows"],
  "properties": {
    "headers": {"type": "array", "items": {"type": "string"}},
    "rows": {"type": "array", "items": {"type": "array", "items": {"type": "string"}}}
  }
}`))

const pdfSystem = `You read the text of one page of a bank statement and return its
transaction table exactly as printed: the column headers, and one row per
transaction with one cell per column, cells copied verbatim (dates, amounts,
signs and descriptions unchanged). A description wrapped over several lines
is one cell. Leave out balances brought forward, totals, page headers and
footers, and anything that is not a transaction. A page without
transactions has no rows; give the headers if the page shows them.`

// ExtractPDF reads a statement PDF's transaction table. The text layer is
// extracted here; the model only says how it is laid out as a table, page
// by page, from text with account numbers and emails masked. A PDF with no
// text (a scan) is ErrUnreadable — there is no OCR in v1 — and one over
// MaxPDFPages is ErrTooManyRows. The rows the model returns must match the
// lines that start with a date to within 10 %, or the file is unreadable.
func ExtractPDF(ctx context.Context, data []byte, client llm.Client) (RawTable, error) {
	pages, err := pdfPages(data)
	if err != nil {
		return RawTable{}, err
	}
	if client == nil {
		return RawTable{}, fmt.Errorf("%w: no model to read PDFs with", ErrUnreadable)
	}

	table := RawTable{Preamble: []string{}, Footer: [][]string{}, Rows: [][]string{}}
	expected := 0
	for i, lines := range pages {
		expected += dateLines(lines)
		page, err := readPDFPage(ctx, client, i+1, lines)
		if err != nil {
			return RawTable{}, fmt.Errorf("%w: page %d: %w", ErrUnreadable, i+1, err)
		}
		if len(table.Headers) == 0 && len(page.Headers) > 0 {
			table.Headers = trimCells(page.Headers)
		}
		for _, row := range page.Rows {
			row = trimCells(row)
			if len(nonEmptyTail(row)) == 0 || sameCells(row, table.Headers) {
				continue // a header repeated on a later page
			}
			table.Rows = append(table.Rows, row)
		}
	}
	if len(table.Headers) == 0 || len(table.Rows) == 0 {
		return RawTable{}, fmt.Errorf("%w: no transaction table found", ErrUnreadable)
	}
	if diff := abs(len(table.Rows) - expected); diff > pdfRowSlack && diff*100 > expected*pdfRowTolerance {
		return RawTable{}, fmt.Errorf("%w: %d rows read, but %d lines start with a date", ErrUnreadable, len(table.Rows), expected)
	}
	width := len(table.Headers)
	for _, row := range table.Rows {
		width = max(width, len(row))
	}
	table.Headers = pad(table.Headers, width)
	for i := range table.Rows {
		table.Rows[i] = pad(table.Rows[i], width)
	}
	return table, nil
}

// pdfPages is the text of each page, as lines in reading order.
func pdfPages(data []byte) ([][]string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	n := reader.NumPage()
	if n > MaxPDFPages {
		return nil, fmt.Errorf("%w: %d pages, at most %d are read", ErrTooManyRows, n, MaxPDFPages)
	}
	pages := make([][]string, 0, n)
	text := 0
	for i := 1; i <= n; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		lines, err := pageLines(page)
		if err != nil {
			return nil, fmt.Errorf("%w: page %d: %w", ErrUnreadable, i, err)
		}
		for _, line := range lines {
			text += len(line)
		}
		pages = append(pages, lines)
	}
	if text == 0 {
		return nil, fmt.Errorf("%w: the PDF has no text (a scan?)", ErrUnreadable)
	}
	return pages, nil
}

// pageLines is a page's text as lines, top to bottom: pieces of text whose
// baselines are within half a point of each other are one line, joined
// left to right with a space where they are apart.
func pageLines(page pdf.Page) (lines []string, err error) {
	defer func() {
		if r := recover(); r != nil { // the reader panics on malformed content
			lines, err = nil, fmt.Errorf("malformed page content: %v", r)
		}
	}()
	texts := page.Content().Text
	sort.SliceStable(texts, func(i, j int) bool {
		if math.Abs(texts[i].Y-texts[j].Y) > 0.5 {
			return texts[i].Y > texts[j].Y
		}
		return texts[i].X < texts[j].X
	})
	var b strings.Builder
	lineY, end := math.Inf(1), 0.0
	flush := func() {
		if line := strings.TrimSpace(b.String()); line != "" {
			lines = append(lines, line)
		}
		b.Reset()
	}
	for _, t := range texts {
		switch {
		case math.Abs(t.Y-lineY) > 0.5:
			flush()
			lineY = t.Y
		case t.X-end > t.FontSize*0.2:
			b.WriteByte(' ')
		}
		b.WriteString(t.S)
		end = t.X + t.W
	}
	flush()
	return lines, nil
}

// dateLines is how many lines start with something date-like, in one,
// two or three words ("14/07/2025", "14 Jul 2025"): a statement's
// transaction lines.
func dateLines(lines []string) int {
	n := 0
	for _, line := range lines {
		words := strings.Fields(line)
		for size := 1; size <= min(3, len(words)); size++ {
			if looksDate(strings.Join(words[:size], " ")) {
				n++
				break
			}
		}
	}
	return n
}

type pdfPageTable struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// readPDFPage asks the model for one page's table, from the page's text
// with identifiers masked line by line.
func readPDFPage(ctx context.Context, client llm.Client, number int, lines []string) (pdfPageTable, error) {
	masked := make([]string, len(lines))
	for i, line := range lines {
		masked[i] = llm.Redact(line)
	}
	result, err := client.CompleteJSON(ctx, llm.Request{
		Purpose:   "statement_pdf",
		System:    pdfSystem,
		Prompt:    fmt.Sprintf("Page %d:\n%s", number, strings.Join(masked, "\n")),
		MaxTokens: min(llm.DefaultMaxTokens, 200+pdfTokensPerLine*len(lines)),
		Effort:    llm.EffortNone,
	}, pdfSchema)
	if err != nil {
		return pdfPageTable{}, err
	}
	var page pdfPageTable
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		return pdfPageTable{}, err
	}
	return page, nil
}

func sameCells(a, b []string) bool {
	a, b = nonEmptyTail(a), nonEmptyTail(b)
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
