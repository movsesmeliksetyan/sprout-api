package categorize_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

func TestLearn_ChangesTheNextClassificationForThatUserOnly(t *testing.T) {
	t.Parallel()
	l := newLedger(t)
	ctx := context.Background()
	food := l.category(t, "Food", "food")
	treats := l.category(t, "Treats", "food")
	other := testutil.NewTestUser(t, l.pool)
	otherFood := testutil.NewCategory(t, l.pool, other.ID, testutil.CategoryType("food")).ID

	before := l.classify(t, debit("TESCO STORES 3297", ""))
	assert.Equal(t, placed{&food, 0.90, categorize.SourceDictionary}, placement(before[0]))

	// The user files a Tesco expense under Treats by hand.
	service := transactions.NewService(l.pool, transactions.WithMerchantRuleHook(categorize.Learn))
	_, err := service.Create(ctx, l.user.User, transactions.CreateParams{
		Kind: transactions.KindExpense, AmountMinor: 500, CategoryID: &treats, Merchant: ptr("Tesco Express"),
	})
	require.NoError(t, err)

	after := l.classify(t, debit("TESCO STORES 3297", ""))
	assert.Equal(t, placed{&treats, 0.99, categorize.SourceUserRule}, placement(after[0]))

	theirs, err := l.engine.Classify(ctx, other.ID, []categorize.Input{debit("TESCO STORES 3297", "")}, categorize.Options{})
	require.NoError(t, err)
	assert.Equal(t, placed{&otherFood, 0.90, categorize.SourceDictionary}, placement(theirs[0]), "another user is unaffected")
}

func TestLearn_IgnoresWhatItCannotFile(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	ctx := context.Background()
	alice := testutil.NewTestUser(t, pool)
	bob := testutil.NewTestUser(t, pool)
	bobs := testutil.NewCategory(t, pool, bob.ID).ID
	mine := testutil.NewCategory(t, pool, alice.ID).ID
	q := db.New(pool)

	require.NoError(t, categorize.Learn(ctx, q, alice.ID, "tesco", bobs))
	require.NoError(t, categorize.Learn(ctx, q, alice.ID, "", mine))
	assert.Equal(t, 0, count(t, pool, `SELECT count(*) FROM merchant_rules`))
}

// voters creates n users who each file key under a category of type.
func voters(t *testing.T, pool *pgxpool.Pool, n int, key, categoryType string) {
	t.Helper()
	for range n {
		user := testutil.NewTestUser(t, pool)
		category := testutil.NewCategory(t, pool, user.ID, testutil.CategoryName("My "+categoryType), testutil.CategoryType(categoryType)).ID
		require.NoError(t, categorize.RecordVotes(context.Background(), db.New(pool), user.ID,
			[]categorize.Vote{{MerchantKey: key, DisplayName: "Zq " + key, CategoryID: category}}, now))
	}
}

func entry(t *testing.T, pool *pgxpool.Pool, key string) (categorize.DictionaryEntry, bool) {
	t.Helper()
	entries, err := categorize.NewStore(db.New(pool)).Dictionary(context.Background(), []string{key}, now)
	require.NoError(t, err)
	e, ok := entries[key]
	if ok && e.Key != key {
		return categorize.DictionaryEntry{}, false
	}
	return e, ok
}

func TestPromoteDictionary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := seeded(t)
	store := categorize.NewStore(db.New(pool))

	// What the model said, which agreeing users replace.
	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "zqcafe", CategoryType: "leisure", DisplayName: "Zqcafe", Confidence: 0.7, Model: "m"},
		{Key: "zqold", CategoryType: "food", DisplayName: "Zqold", Confidence: 0.7, Model: "m", ExpiresAt: now.Add(-time.Minute)},
	}))
	voters(t, pool, 5, "zqcafe", "food")    // 5 of 5: promoted, replacing the llm row
	voters(t, pool, 4, "zqfour", "food")    // too few
	voters(t, pool, 5, "zqsplit", "food")   // 5 of 7 = 71 %: not enough
	voters(t, pool, 2, "zqsplit", "health") //
	voters(t, pool, 5, "zqmost", "food")    // 5 of 6 = 83 %: promoted
	voters(t, pool, 1, "zqmost", "health")  //
	voters(t, pool, 6, "tesco", "food")     // agrees with the seed row: takes the count
	voters(t, pool, 6, "lidl", "shopping")  // disagrees with the seed row: left alone

	result, err := categorize.PromoteDictionary(ctx, pool, now)
	require.NoError(t, err)
	assert.Equal(t, categorize.PromoteResult{Expired: 1, Demoted: 0, Promoted: 3}, result)

	cafe, ok := entry(t, pool, "zqcafe")
	require.True(t, ok)
	assert.Equal(t, categorize.DictionaryEntry{Key: "zqcafe", CategoryType: ptr("food"), DisplayName: "Zq zqcafe", Source: "learned", Votes: 5}, cafe)
	most, ok := entry(t, pool, "zqmost")
	require.True(t, ok)
	assert.Equal(t, 5, most.Votes)
	for _, key := range []string{"zqfour", "zqsplit", "zqold"} {
		_, ok := entry(t, pool, key)
		assert.False(t, ok, key)
	}
	tesco, _ := entry(t, pool, "tesco")
	assert.Equal(t, categorize.DictionaryEntry{Key: "tesco", CategoryType: ptr("food"), DisplayName: "Tesco", Source: "seed", Votes: 6}, tesco)
	lidl, _ := entry(t, pool, "lidl")
	assert.Equal(t, categorize.DictionaryEntry{Key: "lidl", CategoryType: ptr("food"), DisplayName: "Lidl", Source: "seed"}, lidl)

	// Running again changes nothing.
	again, err := categorize.PromoteDictionary(ctx, pool, now)
	require.NoError(t, err)
	assert.Equal(t, categorize.PromoteResult{}, again)

	// Users change their minds: under 60 % now agree with the learned row,
	// and with an llm row that three users voted against.
	voters(t, pool, 4, "zqcafe", "leisure") // 5 of 9 = 56 %
	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "zqmodel", CategoryType: "work", DisplayName: "Zqmodel", Confidence: 0.8, Model: "m"},
		{Key: "zqmodel two", CategoryType: "work", DisplayName: "Zqmodel Two", Confidence: 0.8, Model: "m"},
	}))
	voters(t, pool, 3, "zqmodel", "food")
	voters(t, pool, 2, "zqmodel two", "food") // two voters do not outweigh the model
	result, err = categorize.PromoteDictionary(ctx, pool, now)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Demoted)
	for _, key := range []string{"zqcafe", "zqmodel"} {
		_, ok := entry(t, pool, key)
		assert.False(t, ok, "%s demoted", key)
	}
	_, ok = entry(t, pool, "zqmodel two")
	assert.True(t, ok)
	tesco, _ = entry(t, pool, "tesco")
	assert.Equal(t, "seed", tesco.Source, "a seed row is never demoted")
}

func TestRecordVotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	alice := testutil.NewTestUser(t, pool)
	bob := testutil.NewTestUser(t, pool)
	treats := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Secret Treats"), testutil.CategoryType("food")).ID
	untyped := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Misc")).ID
	fun := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Fun"), testutil.CategoryType("leisure")).ID
	bobs := testutil.NewCategory(t, pool, bob.ID, testutil.CategoryType("food")).ID

	require.NoError(t, categorize.RecordVotes(ctx, q, alice.ID, []categorize.Vote{
		{MerchantKey: "tesco", DisplayName: "Tesco", CategoryID: treats},
		{MerchantKey: "tesco", DisplayName: "Tesco", CategoryID: treats},
		{MerchantKey: "zqmisc", DisplayName: "Zqmisc", CategoryID: untyped},
		{MerchantKey: "zqbob", DisplayName: "Zqbob", CategoryID: bobs},
		{MerchantKey: "", DisplayName: "", CategoryID: treats},
	}, now))
	require.NoError(t, categorize.RecordVotes(ctx, q, alice.ID, []categorize.Vote{
		{MerchantKey: "tesco", DisplayName: "Tesco", CategoryID: fun},
	}, now.Add(time.Hour)))
	require.NoError(t, categorize.RecordVotes(ctx, q, alice.ID, nil, now))

	var key, categoryType string
	require.NoError(t, pool.QueryRow(ctx, `SELECT merchant_key, category_type FROM merchant_votes`).Scan(&key, &categoryType))
	assert.Equal(t, "tesco", key)
	assert.Equal(t, "leisure", categoryType, "one vote a user and merchant, the latest; the type, not the name")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM merchant_votes`))
}
