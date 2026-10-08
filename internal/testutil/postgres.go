// Package testutil is the shared harness for integration tests: a real
// Postgres per test package, an isolated database per test, object storage
// on a real MinIO, and small HTTP helpers.
//
// A package that needs a database adds:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }
//
// and each test calls NewDB.
package testutil

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

const (
	postgresImage    = "postgres:16-alpine"
	templateDatabase = "sprout_template"
)

// server is the package's Postgres container. Creating databases is
// serialised: it is quick, and Postgres refuses to copy a template that
// another session is connected to.
var server struct {
	adminURL string

	mu            sync.Mutex
	templateReady bool
	created       int
}

// Main starts Postgres, runs the package's tests and returns the exit code.
// Without Docker it fails rather than skipping, so a missing database can
// never pass for a green run.
func Main(m *testing.M) int {
	ctx := context.Background()
	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("sprout"),
		postgres.WithPassword("sprout"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: could not start Postgres (is Docker running?): %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "testutil: stop Postgres: %v\n", err)
		}
	}()

	defer stopStore()

	server.adminURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: Postgres connection string: %v\n", err)
		return 1
	}
	return m.Run()
}

// NewDB returns a pool on a database that belongs to this test alone, with
// every migration applied. Tests using it may run in parallel. The database
// is dropped when the test ends.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), createDatabase(t, true))
	require.NoError(t, err)
	// Registered after the drop, so it runs first.
	t.Cleanup(pool.Close)
	return pool
}

// NewEmptyDatabaseURL returns the URL of a database with no migrations
// applied, for tests of the migrations themselves.
func NewEmptyDatabaseURL(t testing.TB) string {
	t.Helper()
	return createDatabase(t, false)
}

func createDatabase(t testing.TB, migrated bool) string {
	t.Helper()
	if server.adminURL == "" {
		t.Fatal("testutil: Postgres is not running; add `func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }` to this package")
	}
	ctx := context.Background()

	server.mu.Lock()
	defer server.mu.Unlock()

	admin, err := pgx.Connect(ctx, server.adminURL)
	require.NoError(t, err)
	defer admin.Close(ctx)

	// Migrating once and copying the result keeps the cost of a test
	// database flat as migrations accumulate.
	if migrated && !server.templateReady {
		_, err := admin.Exec(ctx, "CREATE DATABASE "+templateDatabase)
		require.NoError(t, err)
		require.NoError(t, db.Migrate(ctx, databaseURL(t, templateDatabase), db.MigrateUp, io.Discard))
		server.templateReady = true
	}

	server.created++
	name := fmt.Sprintf("test_%d", server.created)
	statement := "CREATE DATABASE " + name
	if migrated {
		statement += " TEMPLATE " + templateDatabase
	}
	_, err = admin.Exec(ctx, statement)
	require.NoError(t, err)

	t.Cleanup(func() { dropDatabase(t, name) })
	return databaseURL(t, name)
}

func dropDatabase(t testing.TB, name string) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, server.adminURL)
	if err != nil {
		t.Logf("testutil: drop %s: %v", name, err)
		return
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Logf("testutil: drop %s: %v", name, err)
	}
}

func databaseURL(t testing.TB, name string) string {
	t.Helper()
	u, err := url.Parse(server.adminURL)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}
