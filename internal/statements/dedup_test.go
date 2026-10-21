package statements_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/movsesmeliksetyan/sprout-api/internal/statements"
)

func TestDedup(t *testing.T) {
	t.Parallel()
	coffee, lunch := []byte("coffee"), []byte("lunch")
	t1, t2, t3 := uuid.New(), uuid.New(), uuid.New()
	none := uuid.Nil
	for name, tc := range map[string]struct {
		rows   []statements.DedupRow
		ledger []statements.LedgerEntry
		want   []uuid.UUID
	}{
		"nothing in the ledger": {
			rows: []statements.DedupRow{{Hash: coffee}, {Hash: coffee}},
			want: []uuid.UUID{none, none},
		},
		"two coffees, one already in": {
			rows:   []statements.DedupRow{{Hash: coffee}, {Hash: coffee}, {Hash: lunch}},
			ledger: []statements.LedgerEntry{{ID: t1, Hash: coffee}},
			want:   []uuid.UUID{t1, none, none},
		},
		"two coffees, both in": {
			rows:   []statements.DedupRow{{Hash: coffee}, {Hash: coffee}},
			ledger: []statements.LedgerEntry{{ID: t1, Hash: coffee}, {ID: t2, Hash: coffee}},
			want:   []uuid.UUID{t1, t2},
		},
		"the reference wins over the hash": {
			rows:   []statements.DedupRow{{Hash: coffee}, {Hash: coffee, ExternalRef: "F2"}},
			ledger: []statements.LedgerEntry{{ID: t1, Hash: coffee}, {ID: t2, Hash: lunch, ExternalRef: "F2"}},
			want:   []uuid.UUID{t1, t2},
		},
		"a different reference is a different transaction": {
			rows:   []statements.DedupRow{{Hash: coffee, ExternalRef: "F1"}},
			ledger: []statements.LedgerEntry{{ID: t1, Hash: coffee, ExternalRef: "F9"}},
			want:   []uuid.UUID{none},
		},
		"a reference not in the ledger falls back to the hash": {
			rows:   []statements.DedupRow{{Hash: coffee, ExternalRef: "F1"}},
			ledger: []statements.LedgerEntry{{ID: t3, Hash: coffee}},
			want:   []uuid.UUID{t3},
		},
		"a row without a reference matches one with": {
			rows:   []statements.DedupRow{{Hash: coffee}},
			ledger: []statements.LedgerEntry{{ID: t1, Hash: coffee, ExternalRef: "F1"}},
			want:   []uuid.UUID{t1},
		},
		"unreadable rows match nothing": {
			rows:   []statements.DedupRow{{}, {ExternalRef: "F1"}},
			ledger: []statements.LedgerEntry{{ID: t1, ExternalRef: "F1"}},
			want:   []uuid.UUID{none, none},
		},
	} {
		assert.Equal(t, tc.want, statements.Dedup(tc.rows, tc.ledger), name)
	}
}
