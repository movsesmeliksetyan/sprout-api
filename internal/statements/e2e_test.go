package statements_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// expected is a fixture's <name>.expected.json: what reading it must give
// (docs/adding-a-statement-fixture.md).
type expected struct {
	// Bank and Country describe the layout, for the report.
	Bank    string `json:"bank"`
	Country string `json:"country"`
	// MonthFirst reads dates month first when the values cannot tell, as
	// for a US-dollar user.
	MonthFirst bool `json:"month_first,omitempty"`
	// LLM holds the model's replies by schema name, in the order asked; a
	// fixture without one gets no answer from the model.
	LLM map[string][]json.RawMessage `json:"llm,omitempty"`

	Status        string `json:"status"`
	MappingSource string `json:"mapping_source"`
	Rows          int    `json:"rows"`
	ErrorRows     int    `json:"error_rows"`
	ExpenseMinor  int64  `json:"expense_minor"`
	IncomeMinor   int64  `json:"income_minor"`
	FirstDate     string `json:"first_date"`
	LastDate      string `json:"last_date"`
}

// replay is the model for a fixture: it gives the recorded replies for
// each schema in turn and refuses anything else.
type replay struct {
	mu      sync.Mutex
	replies map[string][]json.RawMessage
}

func (r *replay) Complete(_ context.Context, call llm.Call) (llm.Completion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := ""
	if call.Schema != nil {
		name = call.Schema.Name
	}
	if len(r.replies[name]) == 0 {
		return llm.Completion{}, llm.ErrRefused
	}
	reply := r.replies[name][0]
	r.replies[name] = r.replies[name][1:]
	return llm.Completion{Text: string(reply)}, nil
}

// read runs a statement through the pipeline's file stages — detect,
// extract, resolve, transform, validate — as the import processor does.
func read(t *testing.T, data []byte, want expected) expected {
	t.Helper()
	ctx := context.Background()
	replies := map[string][]json.RawMessage{}
	for name, r := range want.LLM {
		replies[name] = append([]json.RawMessage(nil), r...)
	}
	client := llm.New(&replay{replies: replies}, llm.Options{TextModel: "test", MaxAttempts: 1})
	got := expected{Bank: want.Bank, Country: want.Country, MonthFirst: want.MonthFirst, LLM: want.LLM}
	fail := func(err error) expected {
		got.Status, got.MappingSource = "failed", err.Error()
		return got
	}

	detected, err := statements.Detect(data)
	if err != nil {
		return fail(err)
	}
	var rows []statements.Row
	var table statements.RawTable
	switch detected.Format {
	case statements.FormatOFX:
		txs, err := statements.ExtractOFX(detected, time.UTC)
		if err != nil {
			return fail(err)
		}
		for _, tx := range txs {
			rows = append(rows, statements.Row{NormalizedTransaction: tx})
		}
		got.MappingSource = string(statements.SourceNative)
	case statements.FormatCSV:
		table, err = statements.ExtractCSV(detected)
	case statements.FormatXLSX:
		table, err = statements.ExtractXLSX(data)
	case statements.FormatPDF:
		table, err = statements.ExtractPDF(ctx, data, client)
	}
	if err != nil {
		return fail(err)
	}
	if detected.Format != statements.FormatOFX {
		hints := statements.Hints{}
		if want.MonthFirst {
			hints.MonthFirst = &want.MonthFirst
		}
		resolution, err := statements.NewResolver(db.New(testutil.NewDB(t)), statements.WithLLM(client)).
			Resolve(ctx, table, detected.Format, hints)
		if errors.Is(err, statements.ErrNoMapping) {
			got.Status = "needs_mapping"
			return got
		}
		require.NoError(t, err)
		got.MappingSource = string(resolution.Source)
		rows = statements.Transform(table, resolution.Spec, time.UTC)
	}
	validated, err := statements.Validate(rows, statements.Window{}, time.UTC)
	if err != nil {
		return fail(err)
	}
	got.Status = "ready"
	for _, row := range validated.Rows {
		got.Rows++
		if row.Error != "" {
			got.ErrorRows++
			continue
		}
		if row.Direction == statements.Credit {
			got.IncomeMinor += row.AmountMinor
		} else {
			got.ExpenseMinor += row.AmountMinor
		}
		date := row.PostedAt.Format(time.DateOnly)
		if got.FirstDate == "" || date < got.FirstDate {
			got.FirstDate = date
		}
		if date > got.LastDate {
			got.LastDate = date
		}
	}
	return got
}

// TestStatementCorpus reads every statement fixture that has an
// expected.json and checks what came out. At least 90 % must be ready with
// no mapping from the user, and none may fail.
func TestStatementCorpus(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("../../testdata/statements/*/*.expected.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 20, "the corpus")

	type outcome struct{ name, bank, country, status, source string }
	outcomes := make([]outcome, len(files))
	t.Run("files", func(t *testing.T) {
		for i, path := range files {
			name := strings.TrimSuffix(filepath.Base(path), ".expected.json")
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				var want expected
				require.NoError(t, json.Unmarshal(raw, &want))
				matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), name+".*"))
				require.NoError(t, err)
				var statement string
				for _, m := range matches {
					if !strings.HasSuffix(m, ".json") {
						statement = m
					}
				}
				require.NotEmpty(t, statement, "the statement next to %s", path)
				data, err := os.ReadFile(statement)
				require.NoError(t, err)

				got := read(t, data, want)
				outcomes[i] = outcome{filepath.Base(statement), want.Bank, want.Country, got.Status, got.MappingSource}
				if *update {
					encoded, err := json.MarshalIndent(got, "", "  ")
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
					return
				}
				assert.Equal(t, want, got)
			})
		}
	})

	var ready, failed int
	countries := map[string]bool{}
	var report strings.Builder
	for _, o := range outcomes {
		fmt.Fprintf(&report, "%-28s %-14s %-3s %-14s %s\n", o.name, o.bank, o.country, o.status, o.source)
		countries[o.country] = true
		switch o.status {
		case "ready":
			ready++
		case "failed":
			failed++
		}
	}
	t.Logf("statement corpus:\n%s", report.String())
	assert.GreaterOrEqual(t, ready*100, len(files)*90, "ready with no user mapping")
	assert.Zero(t, failed, "none failed")
	assert.GreaterOrEqual(t, len(countries), 8, "countries")
}
