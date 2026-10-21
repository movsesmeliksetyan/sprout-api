package statements

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

const (
	// llmSampleRows is how many rows the model sees.
	llmSampleRows = 10
	// requestSamples is how many values of a column the user sees.
	requestSamples = 3
	// llmMinConfidence is the confidence, in percent, below which the
	// model's mapping is not used.
	llmMinConfidence = 60
)

// ColumnRole is a column and what it holds, as the user or the model
// labels it. Roles are those of contract §2.10 (date, description, amount,
// debit, credit, balance, currency, reference, mcc, ignore), and, for the
// model, direction.
type ColumnRole struct {
	Index int    `json:"index"`
	Role  string `json:"role"`
}

// SpecFromRoles builds a spec from labelled columns, a date format and a
// sign convention. The separators are read from the table's values.
// Contract §2.10's rules apply: exactly one date, at least one
// description, one amount or a debit/credit pair.
func SpecFromRoles(table RawTable, roles []ColumnRole, dateFormat string, sign AmountSign) (MappingSpec, error) {
	spec := MappingSpec{DateFormat: dateFormat, AmountSign: sign, Columns: MappingColumns{Date: -1}}
	var problems []string
	count := map[string]int{}
	for _, cr := range roles {
		index := cr.Index
		count[cr.Role]++
		switch cr.Role {
		case "date":
			spec.Columns.Date = index
		case "description":
			spec.Columns.Description = append(spec.Columns.Description, index)
		case "amount":
			spec.Columns.Amount = &index
		case "debit":
			spec.Columns.Debit = &index
		case "credit":
			spec.Columns.Credit = &index
		case "balance":
			spec.Columns.Balance = &index
		case "currency":
			spec.Columns.Currency = &index
		case "reference":
			spec.Columns.Reference = &index
		case "mcc":
			spec.Columns.MCC = &index
		case "direction":
			spec.Columns.Direction = &index
		case "ignore", "":
		default:
			problems = append(problems, fmt.Sprintf("role %q is not known", cr.Role))
		}
	}
	if count["date"] != 1 {
		problems = append(problems, "exactly one column must be the date")
	}
	for _, single := range []string{"amount", "debit", "credit", "balance", "currency", "reference", "mcc", "direction"} {
		if count[single] > 1 {
			problems = append(problems, fmt.Sprintf("only one column can be the %s", single))
		}
	}
	if spec.AmountSign == "" {
		spec.AmountSign = NegativeIsExpense
	}
	spec.DecimalSeparator, spec.ThousandsSeparator = inferSeparators(table, spec)
	if len(problems) > 0 {
		return spec, fmt.Errorf("%w: %s", ErrInvalidSpec, strings.Join(problems, "; "))
	}
	return spec, nil
}

// llmMappingSchema is the model's reading of a layout.
var llmMappingSchema = llm.MustSchema("statement_mapping", []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["columns", "date_format", "amount_sign", "confidence"],
  "properties": {
    "columns": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["index", "role"],
        "properties": {
          "index": {"type": "integer", "minimum": 0},
          "role": {"type": "string", "enum": ["date", "description", "amount", "debit", "credit", "balance", "currency", "reference", "mcc", "direction", "ignore"]}
        }
      }
    },
    "date_format": {"type": "string"},
    "amount_sign": {"type": "string", "enum": ["negative_is_expense", "positive_is_expense"]},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1}
  }
}`))

const llmMappingSystem = `You work out how to read a bank statement table. Given its column
headers and some rows, say what each column holds: date (the booking or
transaction date, not a value date), description (text about the
transaction; several columns may be), amount (one signed amount), debit and
credit (separate money-out and money-in columns), balance, currency,
reference, mcc, direction (a column that only says debit or credit), or
ignore. Give the date format with the tokens DD, MM, YYYY, YY and MMM and the
separators as they appear (for example DD.MM.YYYY), whether a negative
amount is money out (negative_is_expense) or money in
(positive_is_expense), and how sure you are from 0 to 1.`

// InferMapping asks the model how to read a table from its headers and its
// first rows, cell by cell with identifiers masked. The answer must make a
// spec that reads the sample rows (PRD §4.2), or ErrInvalidSpec.
func InferMapping(ctx context.Context, client llm.Client, table RawTable) (MappingSpec, error) {
	sample := table.Rows[:min(len(table.Rows), llmSampleRows)]
	masked := make([][]string, len(sample))
	for i, row := range sample {
		masked[i] = make([]string, len(row))
		for j, cell := range row {
			masked[i][j] = llm.Redact(cell)
		}
	}
	prompt, err := json.Marshal(struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}{table.Headers, masked})
	if err != nil {
		return MappingSpec{}, err
	}
	result, err := client.CompleteJSON(ctx, llm.Request{
		Purpose: "statement_mapping", System: llmMappingSystem, Prompt: string(prompt), MaxTokens: 1024, Effort: llm.EffortLow,
	}, llmMappingSchema)
	if err != nil {
		return MappingSpec{}, err
	}
	var answer struct {
		Columns    []ColumnRole `json:"columns"`
		DateFormat string       `json:"date_format"`
		AmountSign AmountSign   `json:"amount_sign"`
		Confidence float64      `json:"confidence"`
	}
	if err := json.Unmarshal(result.JSON, &answer); err != nil {
		return MappingSpec{}, err
	}
	if answer.Confidence*100 < llmMinConfidence {
		return MappingSpec{}, fmt.Errorf("%w: the model is not sure (%.2f)", ErrInvalidSpec, answer.Confidence)
	}
	spec, err := SpecFromRoles(table, answer.Columns, answer.DateFormat, answer.AmountSign)
	if err != nil {
		return MappingSpec{}, err
	}
	if err := spec.Check(table, llmSampleRows); err != nil {
		return MappingSpec{}, err
	}
	return spec, nil
}

// MappingRequest is what the user is asked when no mapping was found
// (contract §2.10, mapping_request).
type MappingRequest struct {
	Columns   []RequestColumn   `json:"columns"`
	Suggested MappingSuggestion `json:"suggested"`
	// SampleRows are the table's first rows, kept to check the user's
	// mapping against; the API does not send them.
	SampleRows [][]string `json:"sample_rows,omitempty"`
}

// requestRows is how many rows a mapping request keeps.
const requestRows = 20

// RequestColumn is one column the user labels.
type RequestColumn struct {
	Index         int      `json:"index"`
	Header        string   `json:"header"`
	Samples       []string `json:"samples"`
	SuggestedRole *string  `json:"suggested_role"`
}

// MappingSuggestion is the best guess at a format and sign convention.
type MappingSuggestion struct {
	DateFormat *string     `json:"date_format"`
	AmountSign *AmountSign `json:"amount_sign"`
}

// NewMappingRequest describes a table for the user to label, with the
// heuristics' guesses: each column's header, a few values and a role.
func NewMappingRequest(table RawTable, hints Hints) MappingRequest {
	request := MappingRequest{Columns: make([]RequestColumn, len(table.Headers)), SampleRows: table.Rows[:min(len(table.Rows), requestRows)]}
	guess, _ := Heuristic(table, hints)
	roles := map[int]string{}
	if len(guess.Columns.Description) > 0 {
		roles[guess.Columns.Date] = "date"
		for _, c := range guess.Columns.Description {
			roles[c] = "description"
		}
		for r, c := range map[string]*int{"amount": guess.Columns.Amount, "debit": guess.Columns.Debit, "credit": guess.Columns.Credit,
			"balance": guess.Columns.Balance, "currency": guess.Columns.Currency, "reference": guess.Columns.Reference, "mcc": guess.Columns.MCC} {
			if c != nil {
				roles[*c] = r
			}
		}
	}
	for i, header := range table.Headers {
		values := columnValues(table, i)
		column := RequestColumn{Index: i, Header: header, Samples: values[:min(len(values), requestSamples)]}
		if r, ok := roles[i]; ok {
			column.SuggestedRole = &r
		}
		request.Columns[i] = column
	}
	if guess.DateFormat != "" {
		request.Suggested.DateFormat = &guess.DateFormat
	}
	sign := NegativeIsExpense
	request.Suggested.AmountSign = &sign
	return request
}

// SampleTable is the table a mapping request was made from: its headers
// and first rows. A user's mapping is checked against it.
func (r MappingRequest) SampleTable() RawTable {
	table := RawTable{Headers: make([]string, len(r.Columns)), Rows: r.SampleRows}
	for i, c := range r.Columns {
		table.Headers[i] = c.Header
	}
	return table
}
