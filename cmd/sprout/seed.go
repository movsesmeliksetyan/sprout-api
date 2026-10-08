package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// The ledger `sprout seed` writes unless told otherwise: the dataset the
// aggregation queries are measured on (docs/perf-notes.md).
const (
	defaultSeedSubject      = "seed|performance"
	defaultSeedTransactions = 50_000
	defaultSeedYears        = 3
)

// seedBatch is how many transactions go to the database in one copy.
const seedBatch = 10_000

// seedMerchants is how many different merchants a seeded ledger names.
const seedMerchants = 200

// seedOptions describes the ledger to generate.
type seedOptions struct {
	// Subject is the Auth0 subject of the user who gets the ledger. The user
	// is created, with the default categories, if there is none yet.
	Subject string
	// Transactions is how many to write, spread evenly over the Years
	// before Now.
	Transactions int
	Years        int
	Now          time.Time
}

// runSeed fills one user's ledger with generated transactions. It is a
// development tool: it is left out of the usage text and refuses to run
// against production.
func runSeed(c cli, args []string) error {
	opts := seedOptions{Now: time.Now()}
	flags := flag.NewFlagSet("seed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.Subject, "sub", defaultSeedSubject, "")
	flags.IntVar(&opts.Transactions, "transactions", defaultSeedTransactions, "")
	flags.IntVar(&opts.Years, "years", defaultSeedYears, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || opts.Subject == "" || opts.Transactions < 1 || opts.Years < 1 {
		return &usageError{usage: "sprout seed [-sub <auth0 subject>] [-transactions <n>] [-years <n>]"}
	}
	if env, _ := c.lookup("ENV"); env == string(config.EnvProd) {
		return errors.New("refusing to seed a production database")
	}

	databaseURL, err := config.LoadDatabaseURL(c.lookup)
	if err != nil {
		return err
	}
	pool, err := db.Open(c.ctx, databaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()

	user, err := seedLedger(c.ctx, pool, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "seeded %d transactions over %d years for user %s (%s)\n",
		opts.Transactions, opts.Years, user.ID, opts.Subject)
	return nil
}

// seedLedger writes the ledger opts describes and returns its user. The
// same options always produce the same amounts, dates and merchants. A user
// who already has transactions is left alone, with an error.
func seedLedger(ctx context.Context, database users.Database, opts seedOptions) (db.User, error) {
	user, err := users.NewService(database, users.WithOnUserCreated(categories.Seed)).
		Provision(ctx, auth.Claims{Subject: opts.Subject, Name: "Seed User"})
	if err != nil {
		return db.User{}, err
	}
	q := db.New(database)
	if has, err := q.UserHasTransactions(ctx, user.ID); err != nil {
		return db.User{}, fmt.Errorf("seed: look for transactions: %w", err)
	} else if has {
		return db.User{}, fmt.Errorf("seed: user %s already has transactions", opts.Subject)
	}
	owned, err := q.ListCategories(ctx, db.ListCategoriesParams{UserID: user.ID})
	if err != nil {
		return db.User{}, fmt.Errorf("seed: list categories: %w", err)
	}
	if len(owned) == 0 {
		return db.User{}, fmt.Errorf("seed: user %s has no categories", opts.Subject)
	}
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return db.User{}, fmt.Errorf("seed: timezone %q: %w", user.Timezone, err)
	}

	// Budgets of $150 to $750, so that the summaries have something to
	// measure the spending against.
	ids, budgets := make([]uuid.UUID, len(owned)), make([]int64, len(owned))
	for i, category := range owned {
		ids[i], budgets[i] = category.ID, int64(15_000+10_000*(i%7))
	}
	if err := q.UpdateCategoryBudgets(ctx, db.UpdateCategoryBudgetsParams{UserID: user.ID, Ids: ids, Amounts: budgets}); err != nil {
		return db.User{}, fmt.Errorf("seed: set budgets: %w", err)
	}

	rng := rand.New(rand.NewPCG(uint64(opts.Transactions), uint64(opts.Years)))
	end := opts.Now.UTC().Truncate(time.Second)
	span := end.Sub(end.AddDate(-opts.Years, 0, 0))
	for written := 0; written < opts.Transactions; {
		batch := make([]db.CopyTransactionsParams, 0, min(seedBatch, opts.Transactions-written))
		for range cap(batch) {
			row, err := seedTransaction(rng, user.ID, ids, end.Add(-time.Duration(rng.Int64N(int64(span)))), loc)
			if err != nil {
				return db.User{}, err
			}
			batch = append(batch, row)
		}
		if _, err := q.CopyTransactions(ctx, batch); err != nil {
			return db.User{}, fmt.Errorf("seed: copy transactions: %w", err)
		}
		written += len(batch)
	}
	return user, nil
}

// seedTransaction makes up one transaction at occurredAt: an expense in one
// of the categories nine times in ten, income otherwise.
func seedTransaction(rng *rand.Rand, userID uuid.UUID, categoryIDs []uuid.UUID, occurredAt time.Time, loc *time.Location) (db.CopyTransactionsParams, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return db.CopyTransactionsParams{}, fmt.Errorf("seed: new id: %w", err)
	}
	row := db.CopyTransactionsParams{
		ID:         id,
		UserID:     userID,
		Kind:       string(transactions.KindExpense),
		OccurredAt: occurredAt,
		LocalDate:  period.LocalDate(occurredAt, loc),
		Source:     transactions.SourceManual,
		CreatedAt:  occurredAt,
	}
	if rng.IntN(10) == 0 {
		row.Kind = string(transactions.KindIncome)
		row.AmountMinor = 50_000 + rng.Int64N(350_000)
	} else {
		row.CategoryID = &categoryIDs[rng.IntN(len(categoryIDs))]
		row.AmountMinor = 100 + rng.Int64N(12_000)
	}
	if rng.IntN(4) != 0 {
		merchant := fmt.Sprintf("Merchant %03d", rng.IntN(seedMerchants))
		row.Merchant = &merchant
		row.MerchantKey = transactions.MerchantKey(merchant)
	}
	if rng.IntN(5) == 0 {
		note := fmt.Sprintf("Note %d", rng.IntN(1000))
		row.Note = &note
	}
	row.DedupHash = transactions.DedupHash(row.LocalDate, row.AmountMinor, transactions.Kind(row.Kind), row.MerchantKey)
	return row, nil
}
