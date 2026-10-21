package categorize_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// ledger is a user of a seeded database with categories of their own.
type ledger struct {
	pool   *pgxpool.Pool
	user   testutil.TestUser
	engine *categorize.Engine
	q      *counting
}

func newLedger(t *testing.T) ledger {
	t.Helper()
	pool := seeded(t)
	q := &counting{Querier: db.New(pool)}
	return ledger{
		pool:   pool,
		user:   testutil.NewTestUser(t, pool),
		engine: categorize.NewEngine(q, categorize.WithClock(func() time.Time { return now })),
		q:      q,
	}
}

func (l ledger) category(t *testing.T, name, categoryType string, opts ...testutil.CategoryOption) uuid.UUID {
	t.Helper()
	opts = append([]testutil.CategoryOption{testutil.CategoryName(name), testutil.CategoryType(categoryType)}, opts...)
	return testutil.NewCategory(t, l.pool, l.user.ID, opts...).ID
}

func (l ledger) classify(t *testing.T, in ...categorize.Input) []categorize.Result {
	t.Helper()
	results, err := l.engine.Classify(context.Background(), l.user.ID, in, categorize.Options{})
	require.NoError(t, err)
	require.Len(t, results, len(in))
	return results
}

func debit(description, mcc string) categorize.Input {
	return categorize.Input{RawDescription: description, MCC: mcc, AmountMinor: 1000, Direction: categorize.DirectionDebit}
}

// placed is a result's category, confidence and source.
type placed struct {
	category   *uuid.UUID
	confidence float64
	source     categorize.Source
}

func placement(r categorize.Result) placed {
	return placed{category: r.CategoryID, confidence: r.Confidence, source: r.Source}
}

func TestEngine_Precedence(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	food := l.category(t, "Food", "food")
	treats := l.category(t, "Treats", "food")
	car := l.category(t, "Car", "transport")
	health := l.category(t, "Health", "health")
	store := categorize.NewStore(db.New(l.pool))
	require.NoError(t, store.SaveUserRule(context.Background(), l.user.ID, "tesco", treats, now))
	// Food holds more transactions, so the food type resolves to it.
	testutil.NewTransaction(t, l.pool, l.user.User, testutil.TransactionExpense(food))

	results := l.classify(t,
		// A user rule beats the dictionary (tesco is food), the MCC and the keyword.
		debit("CARD PAYMENT TO TESCO STORES 3297 PHARMACY", "5541"),
		// The dictionary beats the MCC and the keyword.
		debit("LIDL GB LONDON PHARMACY", "5541"),
		// The MCC beats the keyword.
		debit("ZQXV TRADING 1182 PHARMACY", "5541"),
		// The keyword, when nothing else knows.
		debit("ZQXV PHARMACY 1182", ""),
		// Nothing knows.
		debit("ZQXV TRADING 1182", "6011"),
		// Income is never categorised.
		categorize.Input{RawDescription: "TESCO STORES 3297", Direction: categorize.DirectionCredit},
	)

	assert.Equal(t, placed{&treats, 0.99, categorize.SourceUserRule}, placement(results[0]))
	assert.Equal(t, placed{&food, 0.90, categorize.SourceDictionary}, placement(results[1]))
	assert.Equal(t, placed{&car, 0.85, categorize.SourceMCC}, placement(results[2]))
	assert.Equal(t, placed{&health, 0.80, categorize.SourceKeyword}, placement(results[3]))
	assert.Equal(t, placed{nil, 0, categorize.SourceNone}, placement(results[4]))
	assert.Equal(t, placed{nil, 0, categorize.SourceNone}, placement(results[5]))

	assert.Equal(t, "tesco", results[0].MerchantKey)
	assert.Equal(t, "Tesco", results[0].DisplayMerchant)
	assert.Equal(t, "lidl", results[1].MerchantKey)
	assert.Equal(t, "tesco", results[5].MerchantKey, "income keeps its merchant")
}

func TestEngine_ArchivedCategoryFallsThrough(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	food := l.category(t, "Food", "food")
	health := l.category(t, "Health", "health")
	fun := l.category(t, "Fun", "leisure")
	old := l.category(t, "Old", "leisure")
	store := categorize.NewStore(db.New(l.pool))
	require.NoError(t, store.SaveUserRule(context.Background(), l.user.ID, "spotify", old, now))
	_, err := l.pool.Exec(context.Background(), `UPDATE categories SET archived_at = now() WHERE id = ANY($1)`, []uuid.UUID{fun, old})
	require.NoError(t, err)

	results := l.classify(t,
		// The rule points at an archived category and Netflix is leisure, which the
		// user no longer has: the MCC answers.
		debit("SPOTIFY P1234ABCD", "5411"),
		debit("NETFLIX.COM", "5411"),
		// Leisure by the MCC too: the keyword answers.
		debit("ZQXV CINEMA PHARMACY", "7832"),
		// Nothing left that the user has a category for.
		debit("NETFLIX.COM", ""),
	)
	assert.Equal(t, placed{&food, 0.85, categorize.SourceMCC}, placement(results[0]))
	assert.Equal(t, placed{&food, 0.85, categorize.SourceMCC}, placement(results[1]))
	assert.Equal(t, placed{&health, 0.80, categorize.SourceKeyword}, placement(results[2]),
		"cinema is passed over for the next pattern whose type resolves")
	assert.Equal(t, placed{nil, 0, categorize.SourceNone}, placement(results[3]))
}

func TestEngine_SeveralCategoriesOfAType(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	groceries := l.category(t, "Groceries", "food")
	eatingOut := l.category(t, "Eating out", "food")
	snacks := l.category(t, "Snacks", "food")

	// Equal history: the first in display order.
	results := l.classify(t, debit("TESCO", ""))
	assert.Equal(t, &groceries, results[0].CategoryID)

	// The most transactions wins.
	for range 2 {
		testutil.NewTransaction(t, l.pool, l.user.User, testutil.TransactionExpense(eatingOut))
	}
	testutil.NewTransaction(t, l.pool, l.user.User, testutil.TransactionExpense(snacks))
	results = l.classify(t, debit("TESCO", ""))
	assert.Equal(t, &eatingOut, results[0].CategoryID)
}

func TestEngine_ModelAnswersAndVotes(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	food := l.category(t, "Food", "food")
	ctx := context.Background()
	store := categorize.NewStore(db.New(l.pool))
	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "corner bakehouse", CategoryType: "food", DisplayName: "Corner Bakehouse", Confidence: 0.95, Model: "m"},
		{Key: "unsure deli", CategoryType: "food", DisplayName: "Unsure Deli", Confidence: 0.4, Model: "m"},
		{Key: "zqxv", DisplayName: "Zqxv", Confidence: 0.1, Model: "m", ExpiresAt: now.Add(time.Hour)},
	}))
	_, err := l.pool.Exec(ctx, `UPDATE merchant_dictionary SET source = 'learned', votes = 12 WHERE merchant_key = 'aldi'`)
	require.NoError(t, err)
	_, err = l.pool.Exec(ctx, `UPDATE merchant_dictionary SET votes = 3 WHERE merchant_key = 'lidl'`)
	require.NoError(t, err)

	results := l.classify(t,
		debit("CORNER BAKEHOUSE 22", ""),
		debit("UNSURE DELI", ""),
		debit("ZQXV", ""),
		debit("ALDI 551", ""),
		debit("LIDL", ""),
	)
	assert.Equal(t, placed{&food, 0.90, categorize.SourceLLM}, placement(results[0]), "capped at 0.90")
	assert.Equal(t, "Corner Bakehouse", results[0].DisplayMerchant)
	assert.Equal(t, placed{&food, 0.70, categorize.SourceKeyword}, placement(results[1]),
		"below the threshold the next layer runs (deli)")
	assert.Equal(t, placed{nil, 0, categorize.SourceNone}, placement(results[2]))
	assert.Equal(t, placed{&food, 0.97, categorize.SourceDictionary}, placement(results[3]))
	assert.InDelta(t, 0.93, results[4].Confidence, 1e-9)
}

func TestEngine_QueriesDoNotGrowWithTheBatch(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	l.category(t, "Food", "food")

	batch := func(n int) int {
		in := make([]categorize.Input, n)
		for i := range in {
			in[i] = debit(fmt.Sprintf("MERCHANT %s CAFE %d", string(rune('A'+i%26))+string(rune('A'+i/26%26)), i), "5999")
		}
		l.q.queries = 0
		l.classify(t, in...)
		return l.q.queries
	}
	one := batch(1)
	assert.Equal(t, 5, one, "user rules, categories, dictionary, MCC, keywords")
	assert.Equal(t, one, batch(500))
}

func TestEngine_NothingToClassify(t *testing.T) {
	t.Parallel()
	l := newLedger(t)

	results := l.classify(t, categorize.Input{RawDescription: "SALARY", Direction: categorize.DirectionCredit})
	assert.Equal(t, categorize.SourceNone, results[0].Source)
	assert.Equal(t, 0, l.q.queries, "income needs no lookups")

	results = l.classify(t)
	assert.Empty(t, results)
}
