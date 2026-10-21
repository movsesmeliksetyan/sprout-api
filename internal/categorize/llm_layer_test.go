package categorize_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// answer is what the scripted model says about a merchant.
type answer struct {
	Key        string  `json:"merchant_key"`
	IsBusiness bool    `json:"is_business"`
	Type       *string `json:"category_type"`
	Display    string  `json:"display_name"`
	Confidence float64 `json:"confidence"`
}

// model is a fake provider that reads the merchants out of each prompt and
// answers each from its script. A batch holding a key in failOn fails.
type model struct {
	mu      sync.Mutex
	script  map[string]answer
	failOn  map[string]bool
	batches [][]string
}

func newModel() *model {
	return &model{script: map[string]answer{}, failOn: map[string]bool{}}
}

func (m *model) knows(key, categoryType string, confidence float64) {
	m.script[key] = answer{Key: key, IsBusiness: true, Type: &categoryType, Display: "The " + key, Confidence: confidence}
}

func (m *model) Complete(_ context.Context, call llm.Call) (llm.Completion, error) {
	var prompt struct {
		Merchants []struct {
			Key string `json:"merchant_key"`
		} `json:"merchants"`
	}
	if err := json.Unmarshal([]byte(call.Prompt), &prompt); err != nil {
		return llm.Completion{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	out := struct {
		Merchants []answer `json:"merchants"`
	}{Merchants: []answer{}}
	for _, merchant := range prompt.Merchants {
		keys = append(keys, merchant.Key)
	}
	m.batches = append(m.batches, keys)
	for _, merchant := range prompt.Merchants {
		if m.failOn[merchant.Key] {
			return llm.Completion{}, &llm.StatusError{StatusCode: 400, Err: errors.New("bad batch")}
		}
		a, ok := m.script[merchant.Key]
		if !ok {
			a = answer{Key: merchant.Key, IsBusiness: true, Display: merchant.Key, Confidence: 0.1}
		}
		out.Merchants = append(out.Merchants, a)
	}
	text, _ := json.Marshal(out)
	return llm.Completion{Text: string(text), Model: "test-model"}, nil
}

func (m *model) asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for _, batch := range m.batches {
		keys = append(keys, batch...)
	}
	return keys
}

// llmLedger is a ledger whose engine has the scripted model, at a clock
// the test can move.
type llmLedger struct {
	ledger
	model *model
	clock *time.Time
}

func newLLMLedger(t *testing.T) llmLedger {
	t.Helper()
	l := newLedger(t)
	m := newModel()
	clock := now
	l.engine = categorize.NewEngine(l.q,
		categorize.WithClock(func() time.Time { return clock }),
		categorize.WithLLM(llm.New(m, llm.Options{TextModel: "test-model", MaxAttempts: 1}), testutil.Logger(t)))
	return llmLedger{ledger: l, model: m, clock: &clock}
}

func (l llmLedger) classifyLLM(t *testing.T, in ...categorize.Input) []categorize.Result {
	t.Helper()
	results, err := l.engine.Classify(context.Background(), l.user.ID, in, categorize.Options{AllowLLM: true})
	require.NoError(t, err)
	return results
}

func dictionaryRow(t *testing.T, l ledger, key string) (source string, categoryType *string, expires *time.Time, found bool) {
	t.Helper()
	err := l.pool.QueryRow(context.Background(),
		`SELECT source, category_type, expires_at FROM merchant_dictionary WHERE merchant_key = $1`, key).
		Scan(&source, &categoryType, &expires)
	if err != nil {
		return "", nil, nil, false
	}
	return source, categoryType, expires, true
}

func TestLLMLayer_PlacesAndSavesForEveryone(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	food := l.category(t, "Food", "food")
	l.model.knows("corner bakehouse", "food", 0.95)

	results := l.classifyLLM(t, debit("CORNER BAKEHOUSE 22", ""), debit("CORNER BAKEHOUSE 23", ""))
	assert.Equal(t, placed{&food, 0.90, categorize.SourceLLM}, placement(results[0]), "confidence capped at 0.90")
	assert.Equal(t, placed{&food, 0.90, categorize.SourceLLM}, placement(results[1]))
	assert.Equal(t, "The corner bakehouse", results[0].DisplayMerchant)
	assert.Equal(t, []string{"corner bakehouse"}, l.model.asked(), "one question per distinct merchant")

	source, categoryType, expires, found := dictionaryRow(t, l.ledger, "corner bakehouse")
	require.True(t, found)
	assert.Equal(t, "llm", source)
	assert.Equal(t, ptr("food"), categoryType)
	assert.Nil(t, expires)

	// Another user, whose food category is a custom one, gets the answer
	// without a call.
	other := testutil.NewTestUser(t, l.pool)
	groceries := testutil.NewCategory(t, l.pool, other.ID, testutil.CategoryName("Groceries"), testutil.CategoryType("food")).ID
	again, err := l.engine.Classify(context.Background(), other.ID, []categorize.Input{debit("CORNER BAKEHOUSE", "")}, categorize.Options{AllowLLM: true})
	require.NoError(t, err)
	assert.Equal(t, placed{&groceries, 0.90, categorize.SourceLLM}, placement(again[0]))
	assert.Len(t, l.model.asked(), 1, "no second call")
}

func TestLLMLayer_UnknownMerchantIsAskedAgainAfter30Days(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	l.category(t, "Food", "food")

	results := l.classifyLLM(t, debit("ZQXV TRADING", ""))
	assert.Equal(t, categorize.SourceNone, results[0].Source)
	_, categoryType, expires, found := dictionaryRow(t, l.ledger, "zqxv trading")
	require.True(t, found, "an unknown merchant is saved too")
	assert.Nil(t, categoryType)
	require.NotNil(t, expires)
	assert.WithinDuration(t, now.Add(30*24*time.Hour), *expires, time.Second)

	*l.clock = now.Add(29 * 24 * time.Hour)
	l.classifyLLM(t, debit("ZQXV TRADING", ""))
	assert.Len(t, l.model.asked(), 1, "not asked again within 30 days")

	*l.clock = now.Add(31 * 24 * time.Hour)
	l.model.knows("zqxv trading", "food", 0.8)
	results = l.classifyLLM(t, debit("ZQXV TRADING", ""))
	assert.Len(t, l.model.asked(), 2, "asked again once the answer expired")
	assert.Equal(t, categorize.SourceLLM, results[0].Source)
	_, categoryType, expires, _ = dictionaryRow(t, l.ledger, "zqxv trading")
	assert.Equal(t, ptr("food"), categoryType)
	assert.Nil(t, expires, "a placed merchant does not expire")
}

func TestLLMLayer_DropsAndDoesNotSave(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	food := l.category(t, "Food", "food")
	l.model.knows("made up place", "groceries", 0.9) // not a category type
	l.model.script["jane doe"] = answer{Key: "jane doe", IsBusiness: false, Type: ptr("food"), Display: "Jane Doe", Confidence: 0.9}
	l.model.knows("ab", "food", 0.9)

	results := l.classifyLLM(t, debit("MADE UP PLACE", ""), debit("JANE DOE", ""), debit("AB", ""))
	assert.Equal(t, categorize.SourceNone, results[0].Source, "a type the model invented is dropped")
	assert.Equal(t, placed{&food, 0.90, categorize.SourceLLM}, placement(results[1]), "used for this classification")
	assert.Equal(t, placed{&food, 0.90, categorize.SourceLLM}, placement(results[2]))

	for _, key := range []string{"made up place", "jane doe", "ab"} {
		_, _, _, found := dictionaryRow(t, l.ledger, key)
		assert.False(t, found, "%q is not saved", key)
	}
}

func TestLLMLayer_NeverReplacesASeedRow(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	// No food category: the seed row for tesco misses, so the model is asked.
	fun := l.category(t, "Fun", "leisure")
	l.model.knows("tesco", "leisure", 0.9)

	results := l.classifyLLM(t, debit("TESCO STORES 3297", ""))
	assert.Equal(t, placed{&fun, 0.90, categorize.SourceLLM}, placement(results[0]))
	source, categoryType, _, _ := dictionaryRow(t, l.ledger, "tesco")
	assert.Equal(t, "seed", source)
	assert.Equal(t, ptr("food"), categoryType)
}

func TestLLMLayer_PartialBatchFailure(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	food := l.category(t, "Food", "food")

	var in []categorize.Input
	for i := range 60 {
		key := fmt.Sprintf("zqshop%c%c", 'a'+i/26, 'a'+i%26)
		l.model.knows(key, "food", 0.8)
		in = append(in, debit(key, ""))
	}
	l.model.failOn["zqshopca"] = true // in the second batch

	results := l.classifyLLM(t, in...)
	require.Len(t, l.model.batches, 2)
	assert.Len(t, l.model.batches[0], 50, "at most 50 merchants a call")
	assert.Len(t, l.model.batches[1], 10)
	for i, r := range results {
		if i < 50 {
			assert.Equal(t, placed{&food, 0.80, categorize.SourceLLM}, placement(r), "row %d", i)
		} else {
			assert.Equal(t, categorize.SourceNone, r.Source, "row %d of the failed batch", i)
		}
	}
	_, _, _, found := dictionaryRow(t, l.ledger, "zqshopca")
	assert.False(t, found, "a failed batch saves nothing")
}

func TestLLMLayer_OnlyWhenAllowed(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	l.category(t, "Food", "food")
	l.model.knows("corner bakehouse", "food", 0.9)

	results, err := l.engine.Classify(context.Background(), l.user.ID, []categorize.Input{debit("CORNER BAKEHOUSE", "")}, categorize.Options{})
	require.NoError(t, err)
	assert.Equal(t, categorize.SourceNone, results[0].Source)
	assert.Empty(t, l.model.asked())

	// Rows another layer placed are not asked about.
	l.classifyLLM(t, debit("TESCO", ""))
	assert.Empty(t, l.model.asked())
}

func TestLLMLayer_SendsRedactedMerchants(t *testing.T) {
	t.Parallel()
	l := newLLMLedger(t)
	l.category(t, "Food", "food")

	l.classifyLLM(t, categorize.Input{Merchant: "Pay jane@example.com", RawDescription: "TO jane@example.com 4111111111111111", Direction: categorize.DirectionDebit})
	asked := l.model.asked()
	require.Len(t, asked, 1)
	assert.NotContains(t, asked[0], "example")
	assert.NotContains(t, asked[0], "jane")

	n := 0
	require.NoError(t, l.pool.QueryRow(context.Background(), `SELECT count(*) FROM merchant_dictionary WHERE source = 'llm'`).Scan(&n))
	assert.Zero(t, n, "a key that needed masking is not saved")
}
