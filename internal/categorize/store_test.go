package categorize_test

import (
	"context"
	"os"
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

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// now is when the tests look things up.
var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// counting is a Querier that counts the lookup queries made through it.
type counting struct {
	db.Querier
	queries int
}

func (c *counting) ListMerchantRules(ctx context.Context, arg db.ListMerchantRulesParams) ([]db.ListMerchantRulesRow, error) {
	c.queries++
	return c.Querier.ListMerchantRules(ctx, arg)
}

func (c *counting) ListDictionaryEntries(ctx context.Context, arg db.ListDictionaryEntriesParams) ([]db.ListDictionaryEntriesRow, error) {
	c.queries++
	return c.Querier.ListDictionaryEntries(ctx, arg)
}

func (c *counting) ListMCCTypes(ctx context.Context, mccs []string) ([]db.MccMap, error) {
	c.queries++
	return c.Querier.ListMCCTypes(ctx, mccs)
}

func (c *counting) ListTypedCategories(ctx context.Context, userID uuid.UUID) ([]db.ListTypedCategoriesRow, error) {
	c.queries++
	return c.Querier.ListTypedCategories(ctx, userID)
}

func (c *counting) ListKeywordRules(ctx context.Context) ([]db.KeywordRule, error) {
	c.queries++
	return c.Querier.ListKeywordRules(ctx)
}

// seeded is a migrated database with the seed rules loaded.
func seeded(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testutil.NewDB(t)
	_, err := categorize.SeedRules(context.Background(), pool)
	require.NoError(t, err)
	return pool
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func ptr[T any](v T) *T { return &v }

func TestSeedRules_TwiceWithoutDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)

	first, err := categorize.SeedRules(ctx, pool)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, first.Dictionary.Added, 300)
	assert.GreaterOrEqual(t, first.Keywords.Added, 60)
	assert.GreaterOrEqual(t, first.MCC.Added, 500)

	second, err := categorize.SeedRules(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, categorize.SeedResult{}, second)

	assert.Equal(t, first.Dictionary.Added, count(t, pool, `SELECT count(*) FROM merchant_dictionary`))
	assert.Equal(t, first.MCC.Added, count(t, pool, `SELECT count(*) FROM mcc_map`))
	assert.Equal(t, first.Keywords.Added, count(t, pool, `SELECT count(*) FROM keyword_rules`))
}

func TestSeedRules_MakesTheTablesMatchTheFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := seeded(t)

	// A seed row someone changed, a seed row the file no longer has, an MCC
	// and a keyword the files do not have, a learned row and an llm row for
	// seed merchants.
	_, err := pool.Exec(ctx, `UPDATE merchant_dictionary SET category_type = 'work' WHERE merchant_key = 'tesco'`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO merchant_dictionary (id, merchant_key, category_type, display_name, source)
		VALUES ($1, 'gone merchant', 'food', 'Gone Merchant', 'seed')`, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO mcc_map (mcc, category_type) VALUES ('0001', 'food')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO keyword_rules (pattern, category_type, priority) VALUES ('gone', 'food', 1)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE merchant_dictionary SET source = 'learned', category_type = 'shopping', votes = 7
		WHERE merchant_key = 'lidl'`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE merchant_dictionary SET source = 'llm', category_type = 'leisure', confidence = 0.8,
		model = 'm', expires_at = now() + interval '30 days' WHERE merchant_key = 'aldi'`)
	require.NoError(t, err)

	result, err := categorize.SeedRules(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, categorize.SeedResult{
		Dictionary: categorize.SeedCounts{Updated: 2, Removed: 1},
		MCC:        categorize.SeedCounts{Removed: 1},
		Keywords:   categorize.SeedCounts{Removed: 1},
	}, result)

	store := categorize.NewStore(db.New(pool))
	entries, err := store.Dictionary(ctx, []string{"tesco", "lidl", "aldi", "gone merchant"}, now)
	require.NoError(t, err)
	assert.Equal(t, categorize.DictionaryEntry{Key: "tesco", CategoryType: ptr("food"), DisplayName: "Tesco", Source: "seed"}, entries["tesco"])
	assert.Equal(t, categorize.DictionaryEntry{Key: "lidl", CategoryType: ptr("shopping"), DisplayName: "Lidl", Source: "learned", Votes: 7}, entries["lidl"],
		"a promoted row is not replaced by the seed")
	assert.Equal(t, categorize.DictionaryEntry{Key: "aldi", CategoryType: ptr("food"), DisplayName: "Aldi", Source: "seed"}, entries["aldi"],
		"the seed replaces what the model said")
	assert.NotContains(t, entries, "gone merchant")
}

func TestStore_Dictionary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := seeded(t)
	q := &counting{Querier: db.New(pool)}
	store := categorize.NewStore(q)

	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "corner bakehouse", CategoryType: "food", DisplayName: "Corner Bakehouse", Confidence: 0.85, Model: "m"},
		{Key: "zq trading", DisplayName: "Zq Trading", Confidence: 0.2, Model: "m", ExpiresAt: now.Add(30 * 24 * time.Hour)},
		{Key: "old answer", CategoryType: "food", DisplayName: "Old Answer", Confidence: 0.7, Model: "m", ExpiresAt: now.Add(-time.Hour)},
	}))

	keys := []string{"tesco", "tesco petrol station", "uber eats", "uber trip", "corner bakehouse", "zq trading", "old answer", "nobody knows", ""}
	entries, err := store.Dictionary(ctx, keys, now)
	require.NoError(t, err)
	assert.Equal(t, 1, q.queries, "one query for the whole batch")

	assert.Equal(t, "tesco", entries["tesco"].Key)
	assert.Equal(t, "tesco", entries["tesco petrol station"].Key, "longest prefix of whole words")
	assert.Equal(t, "uber eats", entries["uber eats"].Key, "the longer key wins over its prefix")
	assert.Equal(t, ptr("food"), entries["uber eats"].CategoryType)
	assert.Equal(t, "uber", entries["uber trip"].Key)
	assert.Equal(t, ptr("transport"), entries["uber trip"].CategoryType)
	assert.Equal(t, categorize.DictionaryEntry{
		Key: "corner bakehouse", CategoryType: ptr("food"), DisplayName: "Corner Bakehouse", Source: "llm", Confidence: ptr(0.85),
	}, entries["corner bakehouse"])
	assert.Nil(t, entries["zq trading"].CategoryType, "the model did not know it, until the row expires")
	assert.Equal(t, "llm", entries["zq trading"].Source)
	assert.NotContains(t, entries, "old answer", "an expired row answers nothing")
	assert.NotContains(t, entries, "nobody knows")
	assert.NotContains(t, entries, "")

	empty, err := store.Dictionary(ctx, nil, now)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestStore_SaveLLMEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := seeded(t)
	store := categorize.NewStore(db.New(pool))

	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "tesco", CategoryType: "leisure", DisplayName: "Tesco Fun", Confidence: 0.9, Model: "m"},
		{Key: "corner bakehouse", DisplayName: "Corner Bakehouse", Confidence: 0.3, Model: "m1", ExpiresAt: now.Add(time.Hour)},
	}))
	require.NoError(t, store.SaveLLMEntries(ctx, []categorize.LLMEntry{
		{Key: "corner bakehouse", CategoryType: "food", DisplayName: "Corner Bakehouse", Confidence: 0.8, Model: "m2"},
	}))
	require.NoError(t, store.SaveLLMEntries(ctx, nil))

	entries, err := store.Dictionary(ctx, []string{"tesco", "corner bakehouse"}, now.Add(48*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, categorize.DictionaryEntry{Key: "tesco", CategoryType: ptr("food"), DisplayName: "Tesco", Source: "seed"}, entries["tesco"],
		"an llm answer never replaces a seed row")
	assert.Equal(t, categorize.DictionaryEntry{
		Key: "corner bakehouse", CategoryType: ptr("food"), DisplayName: "Corner Bakehouse", Source: "llm", Confidence: ptr(0.8),
	}, entries["corner bakehouse"], "a later answer replaces an llm row, and the zero time does not expire")
	assert.Equal(t, 0, count(t, pool, `SELECT count(*) FROM merchant_dictionary WHERE merchant_key = 'corner bakehouse' AND expires_at IS NOT NULL`))
}

func TestStore_UserRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := &counting{Querier: db.New(pool)}
	store := categorize.NewStore(q)

	alice := testutil.NewTestUser(t, pool)
	bob := testutil.NewTestUser(t, pool)
	food := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Food"))
	treats := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Treats"))
	old := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Old"), testutil.CategoryArchived())
	bobs := testutil.NewCategory(t, pool, bob.ID, testutil.CategoryName("Bob's"))

	require.NoError(t, store.SaveUserRule(ctx, alice.ID, "tesco", food.ID, now))
	require.NoError(t, store.SaveUserRule(ctx, alice.ID, "tesco", treats.ID, now.Add(time.Minute)))
	require.NoError(t, store.SaveUserRule(ctx, bob.ID, "lidl", bobs.ID, now))
	assert.ErrorIs(t, store.SaveUserRule(ctx, alice.ID, "aldi", bobs.ID, now), categorize.ErrCategoryNotFound,
		"another user's category")
	assert.ErrorIs(t, store.SaveUserRule(ctx, alice.ID, "aldi", old.ID, now), categorize.ErrCategoryNotFound,
		"an archived category")

	// A rule saved before its category was archived.
	_, err := pool.Exec(ctx, `INSERT INTO merchant_rules (id, user_id, merchant_key, category_id, last_used_at)
		VALUES ($1, $2, 'netto', $3, $4)`, uuid.Must(uuid.NewV7()), alice.ID, old.ID, now)
	require.NoError(t, err)

	q.queries = 0
	rules, err := store.UserRules(ctx, alice.ID, []string{"tesco", "lidl", "netto", "aldi"})
	require.NoError(t, err)
	assert.Equal(t, 1, q.queries, "one query for the whole batch")
	assert.Equal(t, map[string]uuid.UUID{"tesco": treats.ID}, rules,
		"the latest correction, and nothing of Bob's or for an archived category")

	assert.Equal(t, 2, count(t, pool, `SELECT hits FROM merchant_rules WHERE user_id = $1 AND merchant_key = 'tesco'`, alice.ID))
}

func TestStore_MCCTypesAndKeywords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := seeded(t)
	q := &counting{Querier: db.New(pool)}
	store := categorize.NewStore(q)

	types, err := store.MCCTypes(ctx, []string{"5411", "5541", "5912", "3005", "7011", "0000"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"5411": "food", "5541": "transport", "5912": "health", "3005": "travel", "7011": "travel",
	}, types)

	keywords, err := store.Keywords(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, q.queries, "one query a layer")
	require.GreaterOrEqual(t, len(keywords), 60)
	for i := 1; i < len(keywords); i++ {
		assert.GreaterOrEqual(t, keywords[i-1].Priority, keywords[i].Priority, "highest priority first")
	}
}

func TestKeyword_Matches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"pharmac*", "boots pharmacy", true},
		{"pharmac*", "farmacia central", false},
		{"pharmac*", "pharmac", true},
		{"bar", "bar centrale", true},
		{"bar", "barber shop", false},
		{"car wash", "shiny car wash", true},
		{"car wash", "car and wash", false},
		{"car wash", "car", false},
		{"аптека", "аптека 36 6", true},
		{"filling station", "the filling station co", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, categorize.Keyword{Pattern: tt.pattern}.Matches(tt.key), "%q in %q", tt.pattern, tt.key)
	}
}
