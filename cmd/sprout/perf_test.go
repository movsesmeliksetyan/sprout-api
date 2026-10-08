package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

const (
	// perfBudget is what the database may take, at the 95th percentile, to
	// answer one read endpoint on the seeded ledger.
	perfBudget = 100 * time.Millisecond
	// perfRuns is how often each request is repeated. It is enough for
	// Postgres to consider a generic plan for the prepared statements.
	perfRuns = 40
	// perfOtherUsers each get half a ledger, so that a user's rows are a
	// part of the table, as they are in production.
	perfOtherUsers = 4
	// perfNotesEnv names the file the measurements and plans are written
	// to; see `make perf-notes`.
	perfNotesEnv = "PERF_NOTES"
)

// statement is a query a request ran, with the arguments of its last run.
type statement struct {
	sql  string
	args []any
}

// recorder is a pgx tracer that keeps the statements run through a pool.
type recorder struct {
	mu         sync.Mutex
	statements []statement
}

func (r *recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.statements {
		if r.statements[i].sql == data.SQL {
			r.statements[i].args = data.Args
			return ctx
		}
	}
	r.statements = append(r.statements, statement{sql: data.SQL, args: data.Args})
	return ctx
}

func (r *recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the statements recorded since the last call.
func (r *recorder) take() []statement {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := r.statements
	r.statements = nil
	return taken
}

var queryName = regexp.MustCompile(`-- name: (\w+)`)

func (s statement) name() string {
	if m := queryName.FindStringSubmatch(s.sql); m != nil {
		return m[1]
	}
	return "query"
}

// TestAggregationPerformance holds the five read endpoints to their budget
// on a ledger of 50,000 transactions over three years, and their queries to
// plans that never read the whole transactions table.
func TestAggregationPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 150,000 transactions")
	}
	ctx := context.Background()
	now := time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)
	pool := testutil.NewDB(t)

	user, err := seedLedger(ctx, pool, seedOptions{
		Subject: defaultSeedSubject, Transactions: defaultSeedTransactions, Years: defaultSeedYears, Now: now,
	})
	require.NoError(t, err)
	for i := range perfOtherUsers {
		_, err := seedLedger(ctx, pool, seedOptions{
			Subject: fmt.Sprintf("seed|other-%d", i), Transactions: defaultSeedTransactions / 2, Years: defaultSeedYears, Now: now,
		})
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, "VACUUM (ANALYZE) transactions")
	require.NoError(t, err)

	// The requests go through one traced connection, so every repeat reuses
	// the same prepared statements.
	rec := &recorder{}
	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.ConnConfig.Tracer = rec
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(traced.Close)

	ledger := transactions.NewService(traced)
	summaries := summary.NewService(traced, ledger, summary.WithClock(func() time.Time { return now }))
	owned, err := db.New(pool).ListCategories(ctx, db.ListCategoriesParams{UserID: user.ID})
	require.NoError(t, err)
	category := owned[0].ID

	// A cursor a third of the way into the ledger.
	one, twoYearsAgo := 1, now.AddDate(-2, 0, 0)
	old, err := ledger.List(ctx, user.ID, transactions.ListParams{To: &twoYearsAgo, Limit: &one})
	require.NoError(t, err)
	require.NotNil(t, old.Next)
	rec.take()

	list := func(params transactions.ListParams) func() error {
		return func() error {
			_, err := ledger.List(ctx, user.ID, params)
			return err
		}
	}
	expense := transactions.KindExpense
	from, to := now.AddDate(0, -1, 0), now
	requests := []struct {
		name string
		run  func() error
	}{
		{"GET /home", func() error { _, err := summaries.Home(ctx, user); return err }},
		{"GET /summary/categories?period=week", func() error { _, err := summaries.Categories(ctx, user, "week", nil); return err }},
		{"GET /summary/categories?period=month", func() error { _, err := summaries.Categories(ctx, user, "month", nil); return err }},
		{"GET /summary/categories?period=year", func() error { _, err := summaries.Categories(ctx, user, "year", nil); return err }},
		{"GET /summary/categories/{id}?period=week", func() error { _, err := summaries.Category(ctx, user, category, "week", nil); return err }},
		{"GET /summary/categories/{id}?period=month", func() error { _, err := summaries.Category(ctx, user, category, "month", nil); return err }},
		{"GET /summary/categories/{id}?period=year", func() error { _, err := summaries.Category(ctx, user, category, "year", nil); return err }},
		{"GET /summary/stats?period=week", func() error { _, err := summaries.Stats(ctx, user, "week", nil); return err }},
		{"GET /summary/stats?period=month", func() error { _, err := summaries.Stats(ctx, user, "month", nil); return err }},
		{"GET /summary/stats?period=year", func() error { _, err := summaries.Stats(ctx, user, "year", nil); return err }},
		{"GET /transactions", list(transactions.ListParams{})},
		{"GET /transactions?cursor=", list(transactions.ListParams{After: old.Next})},
		{"GET /transactions?category_id=&from=&to=", list(transactions.ListParams{CategoryID: &category, From: &from, To: &to})},
		{"GET /transactions?kind=expense", list(transactions.ListParams{Kind: &expense})},
		{"GET /transactions?q=merchant 01", list(transactions.ListParams{Query: "merchant 01"})},
	}

	var notes strings.Builder
	var plans strings.Builder
	notes.WriteString("| Request | p95 | Slowest |\n|---|---|---|\n")
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			took := make([]time.Duration, 0, perfRuns)
			for range perfRuns {
				start := time.Now()
				require.NoError(t, request.run())
				took = append(took, time.Since(start))
			}
			slices.Sort(took)
			p95 := took[(perfRuns*95+99)/100-1]
			assert.Less(t, p95, perfBudget, "95th percentile of %d runs", perfRuns)
			fmt.Fprintf(&notes, "| `%s` | %s | %s |\n", request.name,
				p95.Round(10*time.Microsecond), took[perfRuns-1].Round(10*time.Microsecond))

			fmt.Fprintf(&plans, "\n### `%s`\n", request.name)
			for _, s := range rec.take() {
				assert.False(t, scansAllTransactions(t, pool, s), "%s reads the whole transactions table", s.name())
				fmt.Fprintf(&plans, "\n`%s`\n\n```\n%s\n```\n", s.name(), explain(t, pool, s))
			}
		})
	}

	if path := os.Getenv(perfNotesEnv); path != "" {
		require.NoError(t, os.WriteFile(path, []byte(perfNotes(notes.String(), plans.String())), 0o644))
	}
}

// scansAllTransactions reports whether the plan Postgres chooses for s
// contains a sequential scan of the transactions table.
func scansAllTransactions(t *testing.T, pool *pgxpool.Pool, s statement) bool {
	t.Helper()
	var raw string
	err := pool.QueryRow(context.Background(), "EXPLAIN (ANALYZE, FORMAT JSON) "+s.sql, s.args...).Scan(&raw)
	require.NoError(t, err, s.name())
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &plans))
	require.Len(t, plans, 1)

	var scans func(node map[string]any) bool
	scans = func(node map[string]any) bool {
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "transactions" {
			return true
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			if child, ok := child.(map[string]any); ok && scans(child) {
				return true
			}
		}
		return false
	}
	return scans(plans[0].Plan)
}

// explain returns the plan of s as Postgres prints it, with what running it
// took.
func explain(t *testing.T, pool *pgxpool.Pool, s statement) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS) "+s.sql, s.args...)
	require.NoError(t, err, s.name())
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, s.name())
	return strings.Join(lines, "\n")
}

// perfNotes lays out docs/perf-notes.md.
func perfNotes(timings, plans string) string {
	return `# Aggregation performance

Written by ` + "`make perf-notes`" + `; do not edit by hand. It records one run of
` + "`TestAggregationPerformance`" + ` (` + "`cmd/sprout/perf_test.go`" + `), which every
` + "`make test`" + ` repeats and holds to the same budget.

## Dataset

One user with 50,000 transactions spread evenly over three years, written by
` + "`sprout seed`" + `, next to four other users with 25,000 each: 150,000 rows in
` + "`transactions`" + `. Nine in ten are expenses, in the seven default categories.

## Budget

Each read endpoint answers within 100 ms at the database at the 95th
percentile, and no query plan reads the whole ` + "`transactions`" + ` table.

The times below are what one request's queries took together, measured from
the Go side over 40 runs on a single connection, on the machine that wrote
this file. They are an indication, not a benchmark of the hardware.

` + timings + `
## Plans

` + "`EXPLAIN (ANALYZE, BUFFERS)`" + ` of every query each request runs, with the
arguments of its last run.
` + plans
}
