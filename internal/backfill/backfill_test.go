package backfill_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/backfill"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// stored is a transaction's merchant key and dedup hash as stored.
func stored(t *testing.T, pool *pgxpool.Pool, id any) (string, []byte) {
	t.Helper()
	var key string
	var hash []byte
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT merchant_key, dedup_hash FROM transactions WHERE id = $1", id).Scan(&key, &hash))
	return key, hash
}

// oldKey writes the key the way it was computed before the normaliser.
func oldKey(t *testing.T, pool *pgxpool.Pool, id any, key string, hash []byte) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"UPDATE transactions SET merchant_key = $2, dedup_hash = $3 WHERE id = $1", id, key, hash)
	require.NoError(t, err)
}

func run(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	tx, err := sqlDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, backfill.MerchantKeys(context.Background(), tx))
	require.NoError(t, tx.Commit())
}

func TestMerchantKeys(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	user := testutil.NewTestUser(t, pool)
	category := testutil.NewCategory(t, pool, user.ID)
	at := time.Date(2025, 7, 14, 12, 0, 0, 0, time.UTC)

	tesco := testutil.NewTransaction(t, pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(2440), testutil.TransactionAt(at),
		testutil.TransactionMerchant("CARD PAYMENT TO TESCO STORES 3297 LONDON GB"))
	plain := testutil.NewTransaction(t, pool, user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionMerchant("Blue Bottle Coffee"))
	none := testutil.NewTransaction(t, pool, user.User)
	// As the rows were keyed before the normaliser: lower-cased and trimmed.
	staleKey := "card payment to tesco stores 3297 london gb"
	staleHash := transactions.DedupHash(tesco.LocalDate, tesco.AmountMinor, transactions.KindExpense, staleKey)
	oldKey(t, pool, tesco.ID, staleKey, staleHash)
	plainKey, plainHash := stored(t, pool, plain.ID)
	noneKey, noneHash := stored(t, pool, none.ID)

	run(t, pool)

	key, hash := stored(t, pool, tesco.ID)
	assert.Equal(t, "tesco", key)
	assert.Equal(t, transactions.DedupHash(tesco.LocalDate, 2440, transactions.KindExpense, "tesco"), hash,
		"the dedup hash follows the key")
	key, hash = stored(t, pool, plain.ID)
	assert.Equal(t, plainKey, key, "a key that is already right is left alone")
	assert.Equal(t, plainHash, hash)
	key, hash = stored(t, pool, none.ID)
	assert.Equal(t, noneKey, key, "a transaction without a merchant is left alone")
	assert.Equal(t, noneHash, hash)

	run(t, pool)
	key, _ = stored(t, pool, tesco.ID)
	assert.Equal(t, "tesco", key, "running it again changes nothing")
}

func TestMerchantKeys_Pages(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	user := testutil.NewTestUser(t, pool)
	const count = 2500 // more than two pages
	for range count {
		testutil.NewTransaction(t, pool, user.User, testutil.TransactionMerchant("TESCO EXPRESS 4412"))
	}
	_, err := pool.Exec(context.Background(), "UPDATE transactions SET merchant_key = 'stale'")
	require.NoError(t, err)

	run(t, pool)

	var stale int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM transactions WHERE merchant_key <> 'tesco'").Scan(&stale))
	assert.Zero(t, stale, "every page was rewritten")
}

func TestMigrationsAreApplied(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	var applied bool
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 12 AND is_applied)").Scan(&applied))
	assert.True(t, applied, "the test databases run the Go migrations too")
}
