package categories_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func budget(category db.Category, minor int64) map[string]any {
	return map[string]any{"category_id": category.ID, "monthly_budget_minor": minor}
}

func budgets(items []httpx.Category) map[string]int64 {
	out := make(map[string]int64, len(items))
	for _, item := range items {
		out[item.Name] = item.MonthlyBudgetMinor
	}
	return out
}

func TestUpdateBudgets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"), testutil.CategoryBudget(100))
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Car"), testutil.CategoryBudget(200))
	home := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Home"))

	rec := f.do(t, user, http.MethodPut, "/budgets", map[string]any{
		"items": []any{budget(home, 120000), budget(food, 0)},
	})

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	items := testutil.DecodeJSON[httpx.CategoryList](t, rec).Items
	assert.Equal(t, []string{"Food", "Home"}, names(items), "the updated categories, in display order")
	assert.Equal(t, map[string]int64{"Food": 0, "Home": 120000}, budgets(items))
	assert.Equal(t, map[string]int64{"Food": 0, "Car": 200, "Home": 120000}, budgets(f.list(t, user, "")),
		"a category left out keeps its budget")
}

func TestUpdateBudgets_Empty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user := testutil.NewTestUser(t, f.pool)
	testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryBudget(100))

	for _, body := range []map[string]any{{"items": []any{}}, {}} {
		rec := f.do(t, user, http.MethodPut, "/budgets", body)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		assert.JSONEq(t, `{"items":[]}`, rec.Body.String())
	}
	assert.EqualValues(t, 100, f.list(t, user, "")[0].MonthlyBudgetMinor)
}

func TestUpdateBudgets_OneInvalidItemRejectsTheRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	user, other := testutil.NewTestUser(t, f.pool), testutil.NewTestUser(t, f.pool)
	food := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Food"), testutil.CategoryBudget(100))
	car := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Car"), testutil.CategoryBudget(200))
	archived := testutil.NewCategory(t, f.pool, user.ID, testutil.CategoryName("Old"), testutil.CategoryBudget(300), testutil.CategoryArchived())
	foreign := testutil.NewCategory(t, f.pool, other.ID, testutil.CategoryName("Theirs"), testutil.CategoryBudget(400))
	unknown := db.Category{ID: uuid.Must(uuid.NewV7())}

	const notYours = "is not one of your active categories"
	tests := []struct {
		name   string
		items  []any
		fields map[string]string
	}{
		{"negative amount", []any{budget(food, 5000), budget(car, -1)},
			map[string]string{"items.1.monthly_budget_minor": "must not be negative"}},
		{"amount out of range", []any{budget(food, 1_000_000_000_001), budget(car, 5000)},
			map[string]string{"items.0.monthly_budget_minor": "is out of range"}},
		{"another user's category", []any{budget(food, 5000), budget(foreign, 5000)},
			map[string]string{"items.1.category_id": notYours}},
		{"archived category", []any{budget(archived, 5000), budget(food, 5000)},
			map[string]string{"items.0.category_id": notYours}},
		{"unknown category", []any{budget(food, 5000), budget(car, 5000), budget(unknown, 5000)},
			map[string]string{"items.2.category_id": notYours}},
		{"category listed twice", []any{budget(food, 5000), budget(car, 5000), budget(food, 6000)},
			map[string]string{"items.2.category_id": "is listed more than once"}},
		{"several problems", []any{budget(foreign, -1), budget(food, 5000), budget(food, -2)},
			map[string]string{
				"items.0.category_id":          notYours,
				"items.0.monthly_budget_minor": "must not be negative",
				"items.2.category_id":          "is listed more than once",
				"items.2.monthly_budget_minor": "must not be negative",
			}},
	}
	for _, tt := range tests {
		rec := f.do(t, user, http.MethodPut, "/budgets", map[string]any{"items": tt.items})
		body := requireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		assert.Equal(t, tt.fields, body.Error.Fields, tt.name)
	}

	assert.Equal(t, map[string]int64{"Food": 100, "Car": 200, "Old": 300}, budgets(f.list(t, user, "?include_archived=true")),
		"no budget changes")
	assert.EqualValues(t, 400, f.stored(t, other, foreign.ID).MonthlyBudgetMinor)
}
