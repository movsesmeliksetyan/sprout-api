package statements

import (
	"bytes"

	"github.com/google/uuid"
)

// DedupRow is a statement row as de-duplication sees it: its dedup hash
// (transactions.DedupHash; nil for a row that could not be read) and the
// bank's reference for it, if any.
type DedupRow struct {
	Hash        []byte
	ExternalRef string
}

// LedgerEntry is a transaction already in the ledger that a row may
// duplicate.
type LedgerEntry struct {
	ID          uuid.UUID
	Hash        []byte
	ExternalRef string
}

// Dedup finds the rows that are already in the ledger (PRD §4.3 stage 6)
// and returns, for each row, the transaction it duplicates, or uuid.Nil.
// Each transaction is the duplicate of one row at most, so identical rows
// in a file stay unless the ledger has as many: two coffees on one day are
// two coffees. A row with a bank reference is matched on it first; else,
// or without one, on its hash — against transactions with no reference
// when the row has one, since a different reference is a different
// transaction. Earlier rows and older transactions are matched first.
func Dedup(rows []DedupRow, ledger []LedgerEntry) []uuid.UUID {
	matches := make([]uuid.UUID, len(rows))
	claimed := make([]bool, len(ledger))
	claim := func(i int, fits func(LedgerEntry) bool) {
		for j, entry := range ledger {
			if !claimed[j] && fits(entry) {
				claimed[j], matches[i] = true, entry.ID
				return
			}
		}
	}
	for i, row := range rows {
		if row.Hash != nil && row.ExternalRef != "" {
			claim(i, func(e LedgerEntry) bool { return e.ExternalRef == row.ExternalRef })
		}
	}
	for i, row := range rows {
		if row.Hash == nil || matches[i] != uuid.Nil {
			continue
		}
		claim(i, func(e LedgerEntry) bool {
			return bytes.Equal(e.Hash, row.Hash) && (row.ExternalRef == "" || e.ExternalRef == "")
		})
	}
	return matches
}
