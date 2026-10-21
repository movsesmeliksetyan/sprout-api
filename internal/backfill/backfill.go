// Package backfill holds the migrations written in Go: data changes that
// need the application's own code, such as recomputing merchant keys with
// the normaliser. They run with the SQL migrations, in version order;
// register them with db.Migrate.
package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// Migrations are the Go migrations, by version. A version here is never
// used by a SQL file in migrations/.
func Migrations() []*goose.Migration {
	return []*goose.Migration{
		// 00012: merchant keys from the normaliser (BE-29) in place of the
		// lower-cased merchant. Down leaves the keys as they are: either
		// form is a valid key.
		goose.NewGoMigration(12, &goose.GoFunc{RunTx: MerchantKeys}, nil),
	}
}

// pageSize is how many transactions are read and rewritten at a time.
const pageSize = 1000

// MerchantKeys recomputes every transaction's merchant key, and the dedup
// hash made from it, with the current normaliser. Only rows whose key
// changes are written. It is safe to run again, which a change to the
// normaliser's data files calls for: add a migration that runs it.
func MerchantKeys(ctx context.Context, tx *sql.Tx) error {
	after := uuid.Nil
	for {
		page, err := readPage(ctx, tx, after)
		if err != nil {
			return err
		}
		if len(page.ids) > 0 {
			_, err := tx.ExecContext(ctx, `
				UPDATE transactions t
				SET merchant_key = u.key, dedup_hash = u.hash
				FROM unnest($1::uuid[], $2::text[], $3::bytea[]) AS u(id, key, hash)
				WHERE t.id = u.id`, page.ids, page.keys, page.hashes)
			if err != nil {
				return fmt.Errorf("backfill: write merchant keys: %w", err)
			}
		}
		if page.read < pageSize {
			return nil
		}
		after = page.last
	}
}

// page is the changes for one page of transactions.
type page struct {
	ids    []string
	keys   []string
	hashes [][]byte
	// read is how many transactions the page held, last the final one.
	read int
	last uuid.UUID
}

// readPage reads the transactions with a merchant after the given id and
// works out which keys change.
func readPage(ctx context.Context, tx *sql.Tx, after uuid.UUID) (page, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, merchant, merchant_key, kind, amount_minor, local_date
		FROM transactions
		WHERE id > $1 AND merchant IS NOT NULL
		ORDER BY id
		LIMIT $2`, after, pageSize)
	if err != nil {
		return page{}, fmt.Errorf("backfill: read transactions: %w", err)
	}
	defer rows.Close()

	var p page
	for rows.Next() {
		var (
			id               uuid.UUID
			merchant, oldKey string
			kind             string
			amountMinor      int64
			localDate        time.Time
		)
		if err := rows.Scan(&id, &merchant, &oldKey, &kind, &amountMinor, &localDate); err != nil {
			return page{}, fmt.Errorf("backfill: read transaction: %w", err)
		}
		p.read++
		p.last = id
		key := transactions.MerchantKey(merchant)
		if key == oldKey {
			continue
		}
		p.ids = append(p.ids, id.String())
		p.keys = append(p.keys, key)
		p.hashes = append(p.hashes, transactions.DedupHash(localDate, amountMinor, transactions.Kind(kind), key))
	}
	if err := rows.Err(); err != nil {
		return page{}, fmt.Errorf("backfill: read transactions: %w", err)
	}
	return p, nil
}
