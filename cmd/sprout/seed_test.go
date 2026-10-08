package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

func runSeedCommand(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
	code = run(append([]string{"seed"}, args...), cli{ctx: context.Background(), lookup: lookup, stdout: &out, stderr: &errOut})
	return code, out.String(), errOut.String()
}

func TestSeed_WritesALedger(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	env := map[string]string{"ENV": "dev", "DATABASE_URL": pool.Config().ConnString()}

	code, stdout, stderr := runSeedCommand(t, env, "-sub", "seed|test", "-transactions", "500", "-years", "1")

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "seeded 500 transactions over 1 years")

	ctx := context.Background()
	q := db.New(pool)
	user, err := q.GetUserByAuth0Sub(ctx, "seed|test")
	require.NoError(t, err)
	categories, err := q.ListCategories(ctx, db.ListCategoriesParams{UserID: user.ID})
	require.NoError(t, err)
	require.Len(t, categories, 7, "a new user gets the default categories")
	for _, category := range categories {
		assert.Positive(t, category.MonthlyBudgetMinor)
	}

	var count, expenses, uncategorised int
	var oldest, newest time.Time
	err = pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE kind = 'expense'),
		       count(*) FILTER (WHERE kind = 'expense' AND category_id IS NULL),
		       min(occurred_at), max(occurred_at)
		FROM transactions WHERE user_id = $1`, user.ID).Scan(&count, &expenses, &uncategorised, &oldest, &newest)
	require.NoError(t, err)
	assert.Equal(t, 500, count)
	assert.InDelta(t, 450, expenses, 40, "about nine in ten are expenses")
	assert.Zero(t, uncategorised)
	assert.WithinDuration(t, time.Now().AddDate(-1, 0, 0), oldest, 30*24*time.Hour)
	assert.WithinDuration(t, time.Now(), newest, 30*24*time.Hour)

	code, _, stderr = runSeedCommand(t, env, "-sub", "seed|test", "-transactions", "500")

	assert.Equal(t, exitError, code, "a ledger is never seeded twice")
	assert.Contains(t, stderr, "already has transactions")
}

func TestSeed_SameOptionsSameLedger(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 7, 21, 17, 24, 0, 0, time.UTC)
	total := func() int64 {
		pool := testutil.NewDB(t)
		user, err := seedLedger(context.Background(), pool, seedOptions{Subject: "seed|test", Transactions: 300, Years: 2, Now: now})
		require.NoError(t, err)
		net, err := db.New(pool).LedgerNet(context.Background(), user.ID)
		require.NoError(t, err)
		return net
	}

	assert.Equal(t, total(), total())
}

func TestSeed_Refusals(t *testing.T) {
	t.Parallel()
	const usage = "usage: sprout seed"
	dev := map[string]string{"ENV": "dev", "DATABASE_URL": "postgres://sprout:sprout@localhost:5432/sprout"}

	tests := []struct {
		name       string
		env        map[string]string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{"unknown flag", dev, []string{"-user", "x"}, exitUsage, usage},
		{"stray argument", dev, []string{"now"}, exitUsage, usage},
		{"no transactions", dev, []string{"-transactions", "0"}, exitUsage, usage},
		{"no years", dev, []string{"-years", "0"}, exitUsage, usage},
		{"production", map[string]string{"ENV": "prod", "DATABASE_URL": dev["DATABASE_URL"]}, nil, exitError, "refusing to seed a production database"},
		{"no database", map[string]string{"ENV": "dev"}, nil, exitError, "DATABASE_URL: required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runSeedCommand(t, tt.env, tt.args...)

			assert.Equal(t, tt.wantCode, code)
			assert.Contains(t, stderr, tt.wantStderr)
		})
	}
}

func TestSeed_IsNotInTheUsage(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer

	usage(&stdout)

	assert.NotContains(t, stdout.String(), "seed")
}
