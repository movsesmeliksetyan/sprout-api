package categories_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// api is the server's API with only the categories handler implemented.
type api struct {
	*categories.Handler
	rest
}

type rest struct{ httpx.NotImplemented }

// fixture is a server with a real Postgres behind it.
type fixture struct {
	pool   *pgxpool.Pool
	server http.Handler
}

func newFixture(t *testing.T, opts ...categories.Option) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	logger := testutil.Logger(t)
	responder := httpx.NewResponder(logger)
	server := httpx.NewServer(logger,
		httpx.WithAPIMiddleware(
			auth.Middleware(testutil.TokenVerifier(), responder, logger),
			users.Middleware(users.NewService(pool), responder),
		),
		httpx.WithAPI(api{Handler: categories.NewHandler(categories.NewService(pool, opts...))}),
	).Handler()
	return fixture{pool: pool, server: server}
}

func (f fixture) do(t *testing.T, user testutil.TestUser, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Do(f.server, testutil.AuthedRequest(t, user.Claims, method, "/v1"+path, body))
}

// list returns the user's categories as the API lists them.
func (f fixture) list(t *testing.T, user testutil.TestUser, query string) []httpx.Category {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/categories"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return testutil.DecodeJSON[httpx.CategoryList](t, rec).Items
}

func (f fixture) stored(t *testing.T, user testutil.TestUser, id uuid.UUID) db.Category {
	t.Helper()
	category, err := db.New(f.pool).GetCategory(context.Background(), db.GetCategoryParams{ID: id, UserID: user.ID})
	require.NoError(t, err)
	return category
}

type errorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

// requireError checks the status and code of an error response and returns
// its body.
func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	body := testutil.DecodeJSON[errorBody](t, rec)
	require.Equal(t, code, body.Error.Code)
	return body
}

func names(items []httpx.Category) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func ids(items ...db.Category) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func TestListCategories(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	assert.JSONEq(t, `{"items":[]}`, f.do(t, user, http.MethodGet, "/categories", nil).Body.String(),
		"no categories is an empty list, not null")

	food := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"), testutil.CategoryBudget(50000))
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Old"), testutil.CategoryArchived())
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Car"))

	items := f.list(t, user, "")
	assert.Equal(t, []string{"Food", "Car"}, names(items), "archived categories are hidden")
	assert.Equal(t, httpx.Category{
		ID: food.ID, Name: "Food", Icon: httpx.CategoryIconTag, Shade: 0, SortOrder: 0, MonthlyBudgetMinor: 50000,
	}, items[0])

	assert.Equal(t, []string{"Food", "Car"}, names(f.list(t, user, "?include_archived=false")))

	items = f.list(t, user, "?include_archived=true")
	assert.Equal(t, []string{"Food", "Car", "Old"}, names(items), "archived categories come last")
	assert.True(t, items[2].Archived)
}

func TestListCategories_OnlyTheCallersOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	testutil.NewCategory(t, f.pool, other.ID)

	assert.Empty(t, f.list(t, user, "?include_archived=true"))
}

func TestCreateCategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	testutil.NewCategory(t, f.pool, user.ID)

	rec := f.do(t, user, http.MethodPost, "/categories", map[string]any{
		"name": "  Groceries ", "icon": "cart", "shade": 4, "monthly_budget_minor": 30000,
	})

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	created := testutil.DecodeJSON[httpx.Category](t, rec)
	assert.Equal(t, uuid.Version(7), created.ID.Version())
	assert.Equal(t, httpx.Category{
		ID: created.ID, Name: "Groceries", Icon: httpx.CategoryIconCart, Shade: 4, SortOrder: 1, MonthlyBudgetMinor: 30000,
	}, created)

	row := f.stored(t, user, created.ID)
	require.NotNil(t, row.CategoryType)
	assert.Equal(t, "food", *row.CategoryType, "the type is inferred from the name")
}

func TestCreateCategory_Defaults(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	for i := range 9 {
		rec := f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": fmt.Sprintf("Thing %d", i), "icon": "tag"})
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		created := testutil.DecodeJSON[httpx.Category](t, rec)
		assert.Equal(t, i, created.SortOrder, "a new category goes last")
		assert.Equal(t, i%7, created.Shade, "the shade follows the position, round the ramp")
		assert.Zero(t, created.MonthlyBudgetMinor)
		assert.Nil(t, f.stored(t, user, created.ID).CategoryType)
	}
}

func TestCreateCategory_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)

	tests := []struct {
		name   string
		body   map[string]any
		fields map[string]string
	}{
		{"missing name and icon", map[string]any{},
			map[string]string{"name": "must not be empty", "icon": "is not a known icon"}},
		{"blank name", map[string]any{"name": "   ", "icon": "tag"},
			map[string]string{"name": "must not be empty"}},
		{"long name", map[string]any{"name": strings.Repeat("é", 25), "icon": "tag"},
			map[string]string{"name": "must be at most 24 characters"}},
		{"unknown icon", map[string]any{"name": "Pets", "icon": "dragon"},
			map[string]string{"icon": "is not a known icon"}},
		{"shade too high", map[string]any{"name": "Pets", "icon": "paw", "shade": 7},
			map[string]string{"shade": "must be between 0 and 6"}},
		{"negative shade", map[string]any{"name": "Pets", "icon": "paw", "shade": -1},
			map[string]string{"shade": "must be between 0 and 6"}},
		{"negative budget", map[string]any{"name": "Pets", "icon": "paw", "monthly_budget_minor": -1},
			map[string]string{"monthly_budget_minor": "must not be negative"}},
		{"huge budget", map[string]any{"name": "Pets", "icon": "paw", "monthly_budget_minor": 1_000_000_000_001},
			map[string]string{"monthly_budget_minor": "is out of range"}},
		{"everything wrong", map[string]any{"name": "", "icon": "x", "shade": 9, "monthly_budget_minor": -5},
			map[string]string{
				"name": "must not be empty", "icon": "is not a known icon",
				"shade": "must be between 0 and 6", "monthly_budget_minor": "must not be negative",
			}},
	}
	for _, tt := range tests {
		rec := f.do(t, user, http.MethodPost, "/categories", tt.body)
		body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		assert.Equal(t, tt.fields, body.Error.Fields, tt.name)
	}
	assert.Empty(t, f.list(t, user, "?include_archived=true"), "nothing is written")

	rec := f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": strings.Repeat("é", 24), "icon": "tag"})
	assert.Equal(t, http.StatusCreated, rec.Code, "24 characters fit, whatever their size in bytes")
}

func TestCreateCategory_DuplicateName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"))
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Pets"), testutil.CategoryArchived())

	rec := f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": " fOOd ", "icon": "tag"})
	body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	assert.Equal(t, map[string]string{"name": "is already used by another category"}, body.Error.Fields)

	rec = f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": "Pets", "icon": "paw"})
	assert.Equal(t, http.StatusCreated, rec.Code, "an archived category does not hold its name")

	rec = f.do(t, other, http.MethodPost, "/categories", map[string]any{"name": "Food", "icon": "tag"})
	assert.Equal(t, http.StatusCreated, rec.Code, "names are unique per user")
}

func TestCreateCategory_Limit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for range categories.MaxActive - 1 {
		testutil.NewCategory(t, f.pool, user.ID)
	}
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryArchived())

	rec := f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": "Thirtieth", "icon": "tag"})
	require.Equal(t, http.StatusCreated, rec.Code, "archived categories do not count: %s", rec.Body.String())

	rec = f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": "One too many", "icon": "tag"})
	body := requireError(t, rec, http.StatusConflict, "conflict")
	assert.Contains(t, body.Error.Message, "at most 30")

	rec = f.do(t, user, http.MethodPatch, "/categories/"+archived.ID.String(), map[string]any{"archived": false})
	requireError(t, rec, http.StatusConflict, "conflict")
	assert.NotNil(t, f.stored(t, user, archived.ID).ArchivedAt, "a full list takes nothing back from the archive")
}

func TestCreateCategory_ConcurrentRequestsRespectTheLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	for range categories.MaxActive - 2 {
		testutil.NewCategory(t, f.pool, user.ID)
	}

	const requests = 8
	codes := make([]int, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = f.do(t, user, http.MethodPost, "/categories", map[string]any{"name": fmt.Sprintf("New %d", i), "icon": "tag"}).Code
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for _, code := range codes {
		if code == http.StatusCreated {
			created++
		} else {
			assert.Equal(t, http.StatusConflict, code)
		}
	}
	assert.Equal(t, 2, created)

	items := f.list(t, user, "")
	require.Len(t, items, categories.MaxActive)
	for i, item := range items {
		assert.Equal(t, i, item.SortOrder, "no two categories share a position")
	}
}

func TestUpdateCategory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Stuff"), testutil.CategoryBudget(100))
	path := "/categories/" + category.ID.String()

	rec := f.do(t, user, http.MethodPatch, path, map[string]any{"monthly_budget_minor": 25000})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, httpx.Category{
		ID: category.ID, Name: "Stuff", Icon: httpx.CategoryIconTag, MonthlyBudgetMinor: 25000,
	}, testutil.DecodeJSON[httpx.Category](t, rec), "fields left out are unchanged")

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"name": " Petrol ", "icon": "car", "shade": 6})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, httpx.Category{
		ID: category.ID, Name: "Petrol", Icon: httpx.CategoryIconCar, Shade: 6, MonthlyBudgetMinor: 25000,
	}, testutil.DecodeJSON[httpx.Category](t, rec))
	row := f.stored(t, user, category.ID)
	require.NotNil(t, row.CategoryType)
	assert.Equal(t, "transport", *row.CategoryType, "a category without a type gets one when its name gains meaning")

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"name": "Gym"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "transport", *f.stored(t, user, category.ID).CategoryType, "a type, once set, stays")

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, "an empty patch changes nothing")
	assert.Equal(t, "Gym", testutil.DecodeJSON[httpx.Category](t, rec).Name)
}

func TestUpdateCategory_Validation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	category := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"))
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Car"))
	path := "/categories/" + category.ID.String()

	rec := f.do(t, user, http.MethodPatch, path, map[string]any{
		"name": " ", "icon": "dragon", "shade": 7, "monthly_budget_minor": -1,
	})
	body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	assert.Equal(t, map[string]string{
		"name": "must not be empty", "icon": "is not a known icon",
		"shade": "must be between 0 and 6", "monthly_budget_minor": "must not be negative",
	}, body.Error.Fields)

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"name": "CAR"})
	body = requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	assert.Equal(t, map[string]string{"name": "is already used by another category"}, body.Error.Fields)

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"name": "FOOD"})
	require.Equal(t, http.StatusOK, rec.Code, "a category can change the case of its own name")
	assert.Equal(t, "FOOD", testutil.DecodeJSON[httpx.Category](t, rec).Name)

	rec = f.do(t, user, http.MethodPatch, "/categories/not-a-uuid", map[string]any{"name": "X"})
	requireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestUpdateCategory_ArchiveAndRestore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"))
	car := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Car"))
	path := "/categories/" + food.ID.String()

	rec := f.do(t, user, http.MethodPatch, path, map[string]any{"archived": true})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.True(t, testutil.DecodeJSON[httpx.Category](t, rec).Archived)
	assert.Equal(t, []string{"Car"}, names(f.list(t, user, "")))
	archivedAt := f.stored(t, user, food.ID).ArchivedAt
	require.NotNil(t, archivedAt)

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"archived": true, "shade": 3})
	require.Equal(t, http.StatusOK, rec.Code, "an archived category can still be edited")
	assert.Equal(t, archivedAt, f.stored(t, user, food.ID).ArchivedAt, "archiving twice keeps the first time")

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"archived": false})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	restored := testutil.DecodeJSON[httpx.Category](t, rec)
	assert.False(t, restored.Archived)
	assert.Equal(t, int(car.SortOrder)+1, restored.SortOrder)
	assert.Equal(t, []string{"Car", "Food"}, names(f.list(t, user, "")), "a restored category goes last")
}

func TestUpdateCategory_RestoreIntoATakenName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	old := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"), testutil.CategoryArchived())
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("food"))
	path := "/categories/" + old.ID.String()

	rec := f.do(t, user, http.MethodPatch, path, map[string]any{"archived": false})
	body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	assert.Equal(t, map[string]string{"name": "is already used by another category"}, body.Error.Fields)

	rec = f.do(t, user, http.MethodPatch, path, map[string]any{"archived": false, "name": "Old food"})
	require.Equal(t, http.StatusOK, rec.Code, "restoring under a new name works: %s", rec.Body.String())
	assert.Equal(t, []string{"food", "Old food"}, names(f.list(t, user, "")))
}

func TestDeleteCategory(t *testing.T) {
	t.Parallel()
	var used sync.Map
	f := newFixture(t, categories.WithUsageCheck(func(_ context.Context, _ db.Querier, _, categoryID uuid.UUID) (bool, error) {
		_, ok := used.Load(categoryID)
		return ok, nil
	}))
	user := testutil.NewTestUser(t, f.pool)
	unused := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Unused"))
	inUse := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("In use"))
	used.Store(inUse.ID, true)

	rec := f.do(t, user, http.MethodDelete, "/categories/"+unused.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, rec.Body.String())

	rec = f.do(t, user, http.MethodDelete, "/categories/"+inUse.ID.String(), nil)
	body := requireError(t, rec, http.StatusConflict, "conflict")
	assert.Contains(t, body.Error.Message, "Archive it instead")

	assert.Equal(t, []string{"In use"}, names(f.list(t, user, "?include_archived=true")))

	rec = f.do(t, user, http.MethodDelete, "/categories/"+unused.ID.String(), nil)
	requireError(t, rec, http.StatusNotFound, "not_found")
}

func TestReorderCategories(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	a := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("A"))
	b := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("B"))
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Z"), testutil.CategoryArchived())
	c := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("C"))

	rec := f.do(t, user, http.MethodPut, "/categories/order", map[string]any{"ids": ids(c, a, b)})

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	items := testutil.DecodeJSON[httpx.CategoryList](t, rec).Items
	assert.Equal(t, []string{"C", "A", "B"}, names(items))
	for i, item := range items {
		assert.Equal(t, i, item.SortOrder)
	}
	assert.Equal(t, []string{"C", "A", "B"}, names(f.list(t, user, "")))
	assert.Equal(t, archived.SortOrder, f.stored(t, user, archived.ID).SortOrder, "archived categories are left alone")
}

func TestReorderCategories_NeedsTheExactSet(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	a := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("A"))
	b := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("B"))
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Z"), testutil.CategoryArchived())
	foreign := testutil.NewCategory(t, f.pool, other.ID, testutil.CategoryName("Theirs"))
	unknown := uuid.Must(uuid.NewV7())

	tests := []struct {
		name string
		ids  []uuid.UUID
	}{
		{"missing id", ids(b)},
		{"empty", []uuid.UUID{}},
		{"duplicate id", ids(b, b)},
		{"duplicate on top of the full set", ids(b, a, b)},
		{"archived id", ids(b, a, archived)},
		{"archived in place of an active one", ids(b, archived)},
		{"another user's id", ids(b, foreign)},
		{"unknown id", []uuid.UUID{b.ID, unknown}},
	}
	for _, tt := range tests {
		rec := f.do(t, user, http.MethodPut, "/categories/order", map[string]any{"ids": tt.ids})
		body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		assert.Equal(t, map[string]string{"ids": "must list every active category exactly once"}, body.Error.Fields, tt.name)
	}
	rec := f.do(t, user, http.MethodPut, "/categories/order", map[string]any{})
	requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")

	assert.Equal(t, []string{"A", "B"}, names(f.list(t, user, "")), "a refused order changes nothing")
	assert.Equal(t, foreign.SortOrder, f.stored(t, other, foreign.ID).SortOrder)
}

func TestCategories_AnotherUsersCategoryIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	theirs := testutil.NewCategory(t, f.pool, other.ID, testutil.CategoryName("Theirs"), testutil.CategoryBudget(100))
	path := "/categories/" + theirs.ID.String()

	rec := f.do(t, user, http.MethodPatch, path, map[string]any{"name": "Mine", "archived": true})
	requireError(t, rec, http.StatusNotFound, "not_found")
	rec = f.do(t, user, http.MethodDelete, path, nil)
	requireError(t, rec, http.StatusNotFound, "not_found")

	rec = f.do(t, user, http.MethodPatch, "/categories/"+uuid.Must(uuid.NewV7()).String(), map[string]any{"name": "Mine"})
	requireError(t, rec, http.StatusNotFound, "not_found")

	assert.Equal(t, theirs, f.stored(t, other, theirs.ID), "the owner's category is untouched")
}

func TestCategories_NeedAToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := testutil.Do(f.server, testutil.JSONRequest(t, http.MethodGet, "/v1/categories", nil))
	requireError(t, rec, http.StatusUnauthorized, "unauthenticated")
}
