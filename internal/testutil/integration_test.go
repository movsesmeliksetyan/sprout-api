package testutil_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// TestServerAndDatabase is the sample integration test: the real server in
// front of a real, migrated database.
func TestServerAndDatabase(t *testing.T) {
	pool := testutil.NewDB(t)
	server := httpx.NewServer(testutil.Logger(t), httpx.WithReadinessCheck("postgres", db.Ready(pool)))

	t.Run("healthz", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.JSONRequest(t, http.MethodGet, "/healthz", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, map[string]string{"status": "ok"}, testutil.DecodeJSON[map[string]string](t, rec))
	})

	t.Run("readyz runs a query through the pool", func(t *testing.T) {
		rec := testutil.Do(server.Handler(), testutil.JSONRequest(t, http.MethodGet, "/readyz", nil))

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("readyz fails once the database is gone", func(t *testing.T) {
		closed := testutil.NewDB(t)
		closed.Close()
		down := httpx.NewServer(testutil.Logger(t), httpx.WithReadinessCheck("postgres", db.Ready(closed)))

		rec := testutil.Do(down.Handler(), testutil.JSONRequest(t, http.MethodGet, "/readyz", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("a write and read round-trip in a transaction", func(t *testing.T) {
		ctx := context.Background()
		createNotes(t, pool)

		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO notes (body, updated_at) VALUES ('first draft', '2000-01-01T00:00:00Z')")
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE notes SET body = 'final'")
			return err
		})
		require.NoError(t, err)

		var body string
		var updatedAt time.Time
		require.NoError(t, pool.QueryRow(ctx, "SELECT body, updated_at FROM notes").Scan(&body, &updatedAt))
		assert.Equal(t, "final", body)
		assert.WithinDuration(t, time.Now(), updatedAt, time.Minute, "the migration's set_updated_at trigger ran")
	})
}

// createNotes adds a scratch table wired to the trigger function that
// migration 00001 installs, which exists only in a migrated database.
func createNotes(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE notes (body text NOT NULL, updated_at timestamptz NOT NULL);
		CREATE TRIGGER notes_set_updated_at BEFORE UPDATE ON notes
			FOR EACH ROW EXECUTE FUNCTION set_updated_at();`)
	require.NoError(t, err)
}

func noteCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM notes").Scan(&n))
	return n
}

func TestNewDB_EachTestGetsItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	first, second := testutil.NewDB(t), testutil.NewDB(t)
	createNotes(t, first)
	createNotes(t, second)

	_, err := first.Exec(ctx, "INSERT INTO notes VALUES ('only in the first', now())")
	require.NoError(t, err)

	assert.Equal(t, 1, noteCount(t, first))
	assert.Equal(t, 0, noteCount(t, second))

	var firstName, secondName string
	require.NoError(t, first.QueryRow(ctx, "SELECT current_database()").Scan(&firstName))
	require.NoError(t, second.QueryRow(ctx, "SELECT current_database()").Scan(&secondName))
	assert.NotEqual(t, firstName, secondName)
}

func TestNewDB_SafeInParallelTests(t *testing.T) {
	for i := range 8 {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			pool := testutil.NewDB(t)
			createNotes(t, pool)

			for range i + 1 {
				_, err := pool.Exec(context.Background(), "INSERT INTO notes VALUES ('row', now())")
				require.NoError(t, err)
			}

			assert.Equal(t, i+1, noteCount(t, pool), "rows from other parallel tests must not appear")
		})
	}
}

func TestNewDB_IsDroppedWhenTheTestEnds(t *testing.T) {
	var name string
	t.Run("inner", func(t *testing.T) {
		pool := testutil.NewDB(t)
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name))
	})

	var exists bool
	err := testutil.NewDB(t).QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists, "%s should have been dropped", name)
}

func TestNewEmptyDatabaseURL_HasNoMigrations(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testutil.NewEmptyDatabaseURL(t))
	require.NoError(t, err)
	defer conn.Close(ctx)

	var hasTrigger bool
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'set_updated_at')").Scan(&hasTrigger))
	assert.False(t, hasTrigger)
}

func TestJSONRequestAndDecode(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type", r.Header.Get("Content-Type"))
		_, _ = w.Write([]byte(`{"name":"echoed"}`))
	})

	rec := testutil.Do(echo, testutil.JSONRequest(t, http.MethodPost, "/", payload{Name: "sent"}))

	assert.Equal(t, "application/json", rec.Header().Get("X-Content-Type"))
	assert.Equal(t, payload{Name: "echoed"}, testutil.DecodeJSON[payload](t, rec))
}
