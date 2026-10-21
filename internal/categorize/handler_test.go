package categorize_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// api is the server's API with only the categorisation handler.
type api struct {
	*categorize.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

// noModel is a language model that fails the test when it is used.
type noModel struct{ t *testing.T }

func (m noModel) CompleteJSON(context.Context, llm.Request, *llm.Schema) (llm.Result, error) {
	m.t.Error("the language model was called")
	return llm.Result{}, llm.ErrUnavailable
}

func (m noModel) VisionJSON(context.Context, llm.Request, llm.Image, *llm.Schema) (llm.Result, error) {
	m.t.Error("the language model was called")
	return llm.Result{}, llm.ErrUnavailable
}

func suggestServer(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	engine := categorize.NewEngine(db.New(pool), categorize.WithLLM(noModel{t}, logger))
	return httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: categorize.NewHandler(engine)}),
	).Handler()
}

func suggest(t *testing.T, server http.Handler, user testutil.TestUser, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(server, testutil.AuthedRequest(t, user.Claims, http.MethodPost, "/v1/categorization/suggest", body))
}

func TestSuggestCategory(t *testing.T) {
	t.Parallel()
	pool := seeded(t)
	server := suggestServer(t, pool)
	alice := testutil.NewTestUser(t, pool)
	bob := testutil.NewTestUser(t, pool)
	food := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Food"), testutil.CategoryType("food")).ID
	treats := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Treats"), testutil.CategoryType("food")).ID
	health := testutil.NewCategory(t, pool, alice.ID, testutil.CategoryName("Health"), testutil.CategoryType("health")).ID
	bobFood := testutil.NewCategory(t, pool, bob.ID, testutil.CategoryType("food")).ID

	ask := func(user testutil.TestUser, body map[string]any) httpx.CategorySuggestion {
		t.Helper()
		rec := suggest(t, server, user, body)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		return testutil.DecodeJSON[httpx.CategorySuggestion](t, rec)
	}

	assert.Equal(t, httpx.CategorySuggestion{CategoryID: &food, Confidence: 0.90, Source: httpx.CategorizationSource("dictionary")},
		ask(alice, map[string]any{"merchant": "Tesco", "note": nil, "amount_minor": 2440}))

	// One correction, and the user's own rule answers.
	require.NoError(t, categorize.Learn(context.Background(), db.New(pool), alice.ID, "tesco", treats))
	assert.Equal(t, httpx.CategorySuggestion{CategoryID: &treats, Confidence: 0.99, Source: httpx.CategorizationSource("user_rule")},
		ask(alice, map[string]any{"merchant": "TESCO EXPRESS"}))
	assert.Equal(t, &bobFood, ask(bob, map[string]any{"merchant": "Tesco"}).CategoryID, "Alice's rule is hers alone")

	// The note feeds the keywords, and names the merchant when there is none.
	assert.Equal(t, &health, ask(alice, map[string]any{"merchant": "Zqxv", "note": "pharmacy run"}).CategoryID)
	assert.Equal(t, &health, ask(alice, map[string]any{"note": "pharmacy"}).CategoryID)

	// Nothing confident enough, and an empty body: null, from none; the
	// model is never asked.
	assert.Equal(t, httpx.CategorySuggestion{Source: httpx.CategorizationSource("none")}, ask(alice, map[string]any{"merchant": "Zqxv Trading"}))
	assert.Equal(t, httpx.CategorySuggestion{Source: httpx.CategorizationSource("none")}, ask(alice, map[string]any{}))
	rec := suggest(t, server, alice, map[string]any{"merchant": "Zqxv"})
	assert.JSONEq(t, `{"category_id": null, "confidence": 0, "source": "none"}`, rec.Body.String(), "null is present, not omitted")
}

func TestSuggestCategory_Unauthenticated(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	rec := testutil.Do(suggestServer(t, pool), testutil.JSONRequest(t, http.MethodPost, "/v1/categorization/suggest", map[string]any{"merchant": "Tesco"}))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSuggestCategory_Fast(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing is measured without -short and -race")
	}
	t.Parallel()
	pool := seeded(t)
	server := suggestServer(t, pool)
	user := testutil.NewTestUser(t, pool)
	for _, categoryType := range []string{"food", "transport", "health", "leisure", "shopping"} {
		testutil.NewCategory(t, pool, user.ID, testutil.CategoryName(categoryType), testutil.CategoryType(categoryType))
	}
	for range 50 {
		require.NoError(t, categorize.NewStore(db.New(pool)).SaveUserRule(context.Background(), user.ID,
			"zq rule "+uuid.NewString()[:8], testutil.NewCategory(t, pool, user.ID).ID, now))
	}

	merchants := []string{"Tesco", "Zqxv Pharmacy", "Uber", "Nobody Knows", "Netflix"}
	var took []time.Duration
	for i := range 200 {
		start := time.Now()
		rec := suggest(t, server, user, map[string]any{"merchant": merchants[i%len(merchants)], "amount_minor": 1000})
		took = append(took, time.Since(start))
		require.Equal(t, http.StatusOK, rec.Code)
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	p95 := took[len(took)*95/100]
	t.Logf("suggest p95 %s", p95)
	assert.Less(t, p95, 50*time.Millisecond)
}
