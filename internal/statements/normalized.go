package statements

import "time"

// Direction is which way money moved.
type Direction string

// The directions of a statement row.
const (
	// Debit is money out.
	Debit Direction = "debit"
	// Credit is money in.
	Credit Direction = "credit"
)

// NormalizedTransaction is one statement row in the canonical form every
// format is read into (PRD §4.1).
type NormalizedTransaction struct {
	RowIndex int `json:"row_index"`
	// PostedAt is midnight in the user's timezone when the file has only a
	// date.
	PostedAt time.Time `json:"posted_at"`
	// AmountMinor is always positive; Direction says which way.
	AmountMinor int64     `json:"amount_minor"`
	Direction   Direction `json:"direction"`
	// Currency is the file's, else empty for the user's.
	Currency string `json:"currency"`
	// RawDescription is the row's text as in the file, joined from its
	// description columns with single spaces.
	RawDescription string `json:"raw_description"`
	Counterparty   string `json:"counterparty"`
	MCC            string `json:"mcc"`
	// ExternalRef is the bank's own id for the row, such as an OFX FITID.
	ExternalRef  string `json:"external_ref"`
	BalanceAfter *int64 `json:"balance_after"`
}
