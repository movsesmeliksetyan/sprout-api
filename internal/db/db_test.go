package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// adminURL connects to the container's default database; tests create their
// own databases from it so they never share state.
var (
	adminURL  string
	dbCounter atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("sprout"),
		postgres.WithPassword("sprout"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres (is Docker running?): %v\n", err)
		os.Exit(1)
	}
	adminURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "stop postgres: %v\n", err)
	}
	os.Exit(code)
}

// newDatabase creates an empty database and returns its URL.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("test_%d", dbCounter.Add(1))

	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	return strings.Replace(adminURL, "/postgres?", "/"+name+"?", 1)
}

// newPool returns a pool on a fresh database, migrated when migrate is true.
func newPool(t *testing.T, migrate bool) *pgxpool.Pool {
	t.Helper()
	url := newDatabase(t)
	if migrate {
		require.NoError(t, Migrate(context.Background(), url, MigrateUp, &bytes.Buffer{}))
	}
	pool, err := Open(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func migrate(t *testing.T, url, command string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, Migrate(context.Background(), url, command, &out))
	return out.String()
}

func TestMigrate(t *testing.T) {
	url := newDatabase(t)

	assert.Contains(t, migrate(t, url, MigrateStatus), "pending      00001_init.sql")

	assert.Contains(t, migrate(t, url, MigrateUp), "applied      00001_init.sql")
	assert.Equal(t, "no migrations to apply\n", migrate(t, url, MigrateUp), "up is idempotent")
	assert.Contains(t, migrate(t, url, MigrateStatus), "applied      00001_init.sql")

	assert.Contains(t, migrate(t, url, MigrateDown), "rolled back  00001_init.sql")
	assert.Contains(t, migrate(t, url, MigrateStatus), "pending      00001_init.sql")
	assert.Equal(t, "no migrations to roll back\n", migrate(t, url, MigrateDown))

	assert.Contains(t, migrate(t, url, MigrateUp), "applied      00001_init.sql", "down then up round-trips")
}

func TestMigrate_UnknownCommand(t *testing.T) {
	err := Migrate(context.Background(), "postgres://unused", "redo", &bytes.Buffer{})

	assert.ErrorIs(t, err, ErrUnknownMigrateCommand)
}

func TestMigrate_UnreachableDatabaseDoesNotLeakPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Migrate(ctx, "postgres://sprout:hunter2-secret@"+closedAddr(t)+"/sprout?sslmode=disable", MigrateUp, &bytes.Buffer{})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2-secret")
}

func TestInitMigration(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t, true)

	t.Run("pg_trgm is installed", func(t *testing.T) {
		var similarity float32
		require.NoError(t, pool.QueryRow(ctx, "SELECT similarity('tesco', 'tesco stores')").Scan(&similarity))
		assert.Greater(t, similarity, float32(0))
	})

	t.Run("set_updated_at refreshes the column", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			CREATE TABLE scratch (id int PRIMARY KEY, note text, updated_at timestamptz NOT NULL);
			CREATE TRIGGER scratch_set_updated_at BEFORE UPDATE ON scratch
				FOR EACH ROW EXECUTE FUNCTION set_updated_at();
			INSERT INTO scratch VALUES (1, 'before', '2000-01-01T00:00:00Z');`)
		require.NoError(t, err)

		_, err = pool.Exec(ctx, "UPDATE scratch SET note = 'after' WHERE id = 1")
		require.NoError(t, err)

		var updatedAt time.Time
		require.NoError(t, pool.QueryRow(ctx, "SELECT updated_at FROM scratch WHERE id = 1").Scan(&updatedAt))
		assert.WithinDuration(t, time.Now(), updatedAt, time.Minute)
	})
}

func TestPingAndReady(t *testing.T) {
	pool := newPool(t, false)

	got, err := New(pool).Ping(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, got)

	assert.NoError(t, Ready(pool)(context.Background()))
}

func TestOpen_MalformedURL(t *testing.T) {
	_, err := Open(context.Background(), "postgres://sprout:hunter2-secret@localhost:not-a-port/sprout")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2-secret")
}

func TestOpen_DoesNotConnect(t *testing.T) {
	pool, err := Open(context.Background(), "postgres://sprout:sprout@"+closedAddr(t)+"/sprout?sslmode=disable")
	require.NoError(t, err, "an unreachable database must not stop the process from starting")
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()

	assert.Error(t, Ready(pool)(ctx))
	assert.Less(t, time.Since(start), 3*time.Second)
}

// closedAddr returns a loopback address nothing is listening on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	return ln.Addr().String()
}

func newItemsTable(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newPool(t, false)
	_, err := pool.Exec(context.Background(), "CREATE TABLE items (name text PRIMARY KEY)")
	require.NoError(t, err)
	return pool
}

func itemNames(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT name FROM items ORDER BY name")
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}

func insertItem(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx, "INSERT INTO items VALUES ($1)", name)
	return err
}

func TestWithTx(t *testing.T) {
	ctx := context.Background()

	t.Run("commits when fn returns nil", func(t *testing.T) {
		pool := newItemsTable(t)

		err := WithTx(ctx, pool, func(tx pgx.Tx) error { return insertItem(ctx, tx, "kept") })

		require.NoError(t, err)
		assert.Equal(t, []string{"kept"}, itemNames(t, pool))
	})

	t.Run("rolls back and returns fn's error unchanged", func(t *testing.T) {
		pool := newItemsTable(t)
		boom := errors.New("boom")

		err := WithTx(ctx, pool, func(tx pgx.Tx) error {
			require.NoError(t, insertItem(ctx, tx, "discarded"))
			return boom
		})

		assert.Same(t, boom, err)
		assert.Empty(t, itemNames(t, pool))
	})

	t.Run("rolls back on panic and re-panics", func(t *testing.T) {
		pool := newItemsTable(t)

		assert.PanicsWithValue(t, "kaboom", func() {
			_ = WithTx(ctx, pool, func(tx pgx.Tx) error {
				require.NoError(t, insertItem(ctx, tx, "discarded"))
				panic("kaboom")
			})
		})

		assert.Empty(t, itemNames(t, pool))
		assert.EqualValues(t, 0, pool.Stat().AcquiredConns(), "the connection is returned to the pool")
	})

	t.Run("a failed nested call undoes only its own work", func(t *testing.T) {
		pool := newItemsTable(t)

		err := WithTx(ctx, pool, func(tx pgx.Tx) error {
			require.NoError(t, insertItem(ctx, tx, "outer"))
			nested := WithTx(ctx, tx, func(inner pgx.Tx) error {
				require.NoError(t, insertItem(ctx, inner, "inner"))
				return errors.New("inner failed")
			})
			require.Error(t, nested)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"outer"}, itemNames(t, pool))
	})

	t.Run("generated queries run inside the transaction", func(t *testing.T) {
		pool := newItemsTable(t)

		err := WithTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := New(tx).Ping(ctx)
			return err
		})

		assert.NoError(t, err)
	})
}
