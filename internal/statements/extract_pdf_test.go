package statements_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

// pageReader is a fake model that reads a page's table from its text: a
// line starting with a date is a row split on two spaces, the line with
// "Date" is the header.
type pageReader struct {
	mu      sync.Mutex
	prompts []string
	// drop leaves out every row whose text contains it, like a model that
	// misses rows.
	drop string
}

func (r *pageReader) Complete(_ context.Context, call llm.Call) (llm.Completion, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, call.Prompt)
	r.mu.Unlock()
	table := struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}{Headers: []string{}, Rows: [][]string{}}
	for _, line := range strings.Split(call.Prompt, "\n")[1:] {
		cells := strings.Split(line, " | ")
		switch {
		case strings.HasPrefix(line, "Date"):
			table.Headers = cells
		case len(line) > 2 && line[2] == '/' && (r.drop == "" || !strings.Contains(line, r.drop)):
			table.Rows = append(table.Rows, cells)
		}
	}
	text, _ := json.Marshal(table)
	return llm.Completion{Text: string(text)}, nil
}

func newPageReader(r *pageReader) llm.Client {
	return llm.New(r, llm.Options{TextModel: "test", MaxAttempts: 1})
}

func TestExtractPDF(t *testing.T) {
	t.Parallel()
	page1 := []string{
		"Example Bank plc - Statement of account ****1234",
		"Account holder: A Rivera   Sort code 00-00-00   Account 12345678",
		"Date | Description | Paid out | Paid in | Balance",
		"14/07/2025 | TESCO STORES 3297 | 42.80 |  | 1203.11",
		"13/07/2025 | PRET A MANGER | 4.50 |  | 1245.91",
		"Page 1 of 2",
	}
	page2 := []string{
		"Date | Description | Paid out | Paid in | Balance",
		"12/07/2025 | SALARY ACME | | 2500.00 | 1250.41",
		"Closing balance 1203.11",
	}

	t.Run("one page", func(t *testing.T) {
		t.Parallel()
		reader := &pageReader{}
		table, err := statements.ExtractPDF(context.Background(), makePDF([][]string{page1}), newPageReader(reader))
		require.NoError(t, err)
		assert.Equal(t, []string{"Date", "Description", "Paid out", "Paid in", "Balance"}, table.Headers)
		assert.Equal(t, [][]string{
			{"14/07/2025", "TESCO STORES 3297", "42.80", "", "1203.11"},
			{"13/07/2025", "PRET A MANGER", "4.50", "", "1245.91"},
		}, table.Rows)
		require.Len(t, reader.prompts, 1)
		assert.NotContains(t, reader.prompts[0], "12345678", "account numbers are masked before the model sees them")
	})

	t.Run("pages merged, repeated header dropped", func(t *testing.T) {
		t.Parallel()
		reader := &pageReader{}
		table, err := statements.ExtractPDF(context.Background(), makePDF([][]string{page1, page2}), newPageReader(reader))
		require.NoError(t, err)
		assert.Len(t, reader.prompts, 2, "one call a page")
		assert.Len(t, table.Rows, 3)
		assert.Equal(t, "SALARY ACME", table.Rows[2][1])
	})

	t.Run("rows that do not add up", func(t *testing.T) {
		t.Parallel()
		_, err := statements.ExtractPDF(context.Background(), makePDF([][]string{page1, page2}), newPageReader(&pageReader{drop: "PRET"}))
		assert.ErrorIs(t, err, statements.ErrUnreadable)
	})

	t.Run("a scan", func(t *testing.T) {
		t.Parallel()
		reader := &pageReader{}
		_, err := statements.ExtractPDF(context.Background(), makePDF([][]string{{}, {}}), newPageReader(reader))
		assert.ErrorIs(t, err, statements.ErrUnreadable)
		assert.Empty(t, reader.prompts, "the model is not asked")
	})

	t.Run("too many pages", func(t *testing.T) {
		t.Parallel()
		pages := make([][]string, statements.MaxPDFPages+1)
		for i := range pages {
			pages[i] = page2
		}
		_, err := statements.ExtractPDF(context.Background(), makePDF(pages), newPageReader(&pageReader{}))
		assert.ErrorIs(t, err, statements.ErrTooManyRows)
	})

	t.Run("the model fails", func(t *testing.T) {
		t.Parallel()
		fake := llm.NewFake() // has no replies
		_, err := statements.ExtractPDF(context.Background(), makePDF([][]string{page1}), llm.New(fake, llm.Options{MaxAttempts: 1}))
		assert.ErrorIs(t, err, statements.ErrUnreadable)
	})

	t.Run("not a PDF", func(t *testing.T) {
		t.Parallel()
		_, err := statements.ExtractPDF(context.Background(), []byte("%PDF-1.4 broken"), newPageReader(&pageReader{}))
		assert.ErrorIs(t, err, statements.ErrUnreadable)
	})
}
