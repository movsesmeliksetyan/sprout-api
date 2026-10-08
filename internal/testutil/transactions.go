package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// TransactionOption changes the transaction NewTransaction creates.
type TransactionOption func(*db.CreateTransactionParams)

// TransactionExpense files the transaction as an expense under the category,
// in place of income.
func TransactionExpense(categoryID uuid.UUID) TransactionOption {
	return func(p *db.CreateTransactionParams) {
		p.Kind = string(transactions.KindExpense)
		p.CategoryID = &categoryID
	}
}

// TransactionAmount sets the amount, which is 1000 otherwise.
func TransactionAmount(minor int64) TransactionOption {
	return func(p *db.CreateTransactionParams) { p.AmountMinor = minor }
}

// TransactionMerchant sets the merchant, which is none otherwise.
func TransactionMerchant(merchant string) TransactionOption {
	return func(p *db.CreateTransactionParams) { p.Merchant = &merchant }
}

// TransactionNote sets the note, which is none otherwise.
func TransactionNote(note string) TransactionOption {
	return func(p *db.CreateTransactionParams) { p.Note = &note }
}

// TransactionAt sets when the transaction happened, which is now otherwise.
func TransactionAt(occurredAt time.Time) TransactionOption {
	return func(p *db.CreateTransactionParams) { p.OccurredAt = occurredAt }
}

// TransactionSource sets how the transaction entered the ledger, which is by
// hand otherwise.
func TransactionSource(source string) TransactionOption {
	return func(p *db.CreateTransactionParams) { p.Source = source }
}

// NewTransaction stores a transaction for the user: income of 1000 entered
// by hand just now, unless the options say otherwise. Its local date, merchant
// key and dedup hash are derived the way the service derives them.
func NewTransaction(t testing.TB, database db.DBTX, user db.User, opts ...TransactionOption) db.Transaction {
	t.Helper()

	id, err := uuid.NewV7()
	require.NoError(t, err)
	params := db.CreateTransactionParams{
		ID:          id,
		UserID:      user.ID,
		Kind:        string(transactions.KindIncome),
		AmountMinor: 1000,
		OccurredAt:  time.Now(),
		Source:      transactions.SourceManual,
	}
	for _, opt := range opts {
		opt(&params)
	}

	loc, err := time.LoadLocation(user.Timezone)
	require.NoError(t, err)
	params.LocalDate = period.LocalDate(params.OccurredAt, loc)
	if params.Merchant != nil {
		params.MerchantKey = transactions.MerchantKey(*params.Merchant)
	}
	params.DedupHash = transactions.DedupHash(params.LocalDate, params.AmountMinor,
		transactions.Kind(params.Kind), params.MerchantKey)

	transaction, err := db.New(database).CreateTransaction(context.Background(), params)
	require.NoError(t, err)
	return transaction
}
