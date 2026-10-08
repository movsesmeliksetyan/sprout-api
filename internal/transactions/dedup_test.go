package transactions_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

func TestMerchantKey(t *testing.T) {
	t.Parallel()
	tests := []struct{ merchant, want string }{
		{"Tesco", "tesco"},
		{"  Blue Bottle Coffee ", "blue bottle coffee"},
		{"ÉCOLE", "école"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, transactions.MerchantKey(tt.merchant), "merchant %q", tt.merchant)
	}
}

func TestDedupHash(t *testing.T) {
	t.Parallel()
	day := time.Date(2025, 7, 21, 0, 0, 0, 0, time.UTC)
	base := transactions.DedupHash(day, 2440, transactions.KindExpense, "tesco")

	assert.Len(t, base, 32)
	assert.Equal(t, base, transactions.DedupHash(day, 2440, transactions.KindExpense, "tesco"), "same inputs")

	different := map[string][]byte{
		"date":     transactions.DedupHash(day.AddDate(0, 0, 1), 2440, transactions.KindExpense, "tesco"),
		"amount":   transactions.DedupHash(day, 2441, transactions.KindExpense, "tesco"),
		"kind":     transactions.DedupHash(day, 2440, transactions.KindIncome, "tesco"),
		"merchant": transactions.DedupHash(day, 2440, transactions.KindExpense, "asda"),
	}
	for name, hash := range different {
		assert.NotEqual(t, base, hash, "a different %s", name)
	}
}
