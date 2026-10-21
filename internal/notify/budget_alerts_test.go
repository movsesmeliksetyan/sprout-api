package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/notify"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// budgets checks budgets in July 2025, on the 21st.
type budgets struct {
	fixture
	service *notify.Service
	user    testutil.TestUser
}

func newBudgets(t *testing.T) budgets {
	t.Helper()
	f := newFixture(t)
	now := time.Date(2025, 7, 21, 12, 0, 0, 0, time.UTC)
	return budgets{
		fixture: f,
		service: notify.NewService(f.pool, f.queue, notify.WithClock(func() time.Time { return now })),
		user:    testutil.NewTestUser(t, f.pool),
	}
}

func (b budgets) category(t *testing.T, name string, budgetMinor int64) db.Category {
	t.Helper()
	return testutil.NewCategory(t, b.pool, b.user.ID, testutil.CategoryName(name), testutil.CategoryBudget(budgetMinor))
}

func (b budgets) spend(t *testing.T, category db.Category, minor int64, day int) db.Transaction {
	t.Helper()
	return testutil.NewTransaction(t, b.pool, b.user.User, testutil.TransactionExpense(category.ID),
		testutil.TransactionAmount(minor), testutil.TransactionAt(time.Date(2025, 7, day, 12, 0, 0, 0, time.UTC)))
}

func (b budgets) check(t *testing.T, importID *uuid.UUID) int {
	t.Helper()
	sent, err := b.service.CheckBudgets(context.Background(), b.user.ID, importID)
	require.NoError(t, err)
	return sent
}

func TestCheckBudgets_EightyThenHundred(t *testing.T) {
	t.Parallel()
	b := newBudgets(t)
	food := b.category(t, "Food", 10000)

	b.spend(t, food, 5000, 2)
	assert.Zero(t, b.check(t, nil), "half the budget is no news")
	b.spend(t, food, 3500, 9)
	assert.Equal(t, 1, b.check(t, nil))
	assert.Zero(t, b.check(t, nil), "told once")
	b.spend(t, food, 2500, 15)
	assert.Equal(t, 1, b.check(t, nil))
	assert.Zero(t, b.check(t, nil))

	inbox := b.inbox(t, b.user)
	require.Len(t, inbox, 2, "exactly two alerts")
	over, near := inbox[0], inbox[1]
	assert.Equal(t, "Food is at 85% of its budget", near.Title)
	assert.Equal(t, "$15 left of your $100 limit this month.", near.Body)
	assert.Equal(t, "budget:"+food.ID.String()+":2025-07:80", near.DedupKey)
	assert.Equal(t, "Food is over budget", over.Title)
	assert.Equal(t, "You're $10 over your $100 limit this month.", over.Body)
	assert.Equal(t, "budget:"+food.ID.String()+":2025-07:100", over.DedupKey)
	assert.Equal(t, "sprout://category/"+food.ID.String(), over.DeepLink)
	assert.Equal(t, "budget_alert", over.Kind)
	assert.Len(t, b.queue.enqueued(), 2, "both are pushed")
}

func TestCheckBudgets_DeletingAndAddingAgainDoesNotResend(t *testing.T) {
	t.Parallel()
	b := newBudgets(t)
	food := b.category(t, "Food", 10000)
	spent := b.spend(t, food, 9000, 3)
	require.Equal(t, 1, b.check(t, nil))

	b.exec(t, `DELETE FROM transactions WHERE id = $1`, spent.ID)
	assert.Zero(t, b.check(t, nil))
	b.spend(t, food, 9000, 3)
	assert.Zero(t, b.check(t, nil), "80 % was told this month already")
	assert.Len(t, b.inbox(t, b.user), 1)
}

func TestCheckBudgets_PastBothAtOnce(t *testing.T) {
	t.Parallel()
	b := newBudgets(t)
	food := b.category(t, "Food", 10000)
	big := b.spend(t, food, 10000, 4)
	require.Equal(t, 1, b.check(t, nil))
	inbox := b.inbox(t, b.user)
	require.Len(t, inbox, 1, "only that it is full")
	assert.Equal(t, "Food has reached its budget", inbox[0].Title)
	assert.Equal(t, "You've used all of your $100 limit this month.", inbox[0].Body)

	b.exec(t, `DELETE FROM transactions WHERE id = $1`, big.ID)
	b.spend(t, food, 9000, 4)
	assert.Zero(t, b.check(t, nil), "dropping back to 90 % does not tell 80 %")
}

func TestCheckBudgets_Ignores(t *testing.T) {
	t.Parallel()
	b := newBudgets(t)
	none := b.category(t, "No budget", 0)
	b.spend(t, none, 50000, 5)
	old := b.category(t, "Bills", 10000)
	testutil.NewTransaction(t, b.pool, b.user.User, testutil.TransactionExpense(old.ID), testutil.TransactionAmount(20000),
		testutil.TransactionAt(time.Date(2025, 6, 20, 12, 0, 0, 0, time.UTC)))
	other := testutil.NewTestUser(t, b.pool)
	theirs := testutil.NewCategory(t, b.pool, other.ID, testutil.CategoryBudget(100))
	testutil.NewTransaction(t, b.pool, other.User, testutil.TransactionExpense(theirs.ID), testutil.TransactionAmount(5000),
		testutil.TransactionAt(time.Date(2025, 7, 5, 12, 0, 0, 0, time.UTC)))

	assert.Zero(t, b.check(t, nil), "no budget, last month and someone else's spending")
	assert.Empty(t, b.inbox(t, b.user))
}

func TestCheckBudgets_ImportCombines(t *testing.T) {
	t.Parallel()
	b := newBudgets(t)
	food := b.category(t, "Food", 10000)
	bills := b.category(t, "Bills", 10000)
	travel := b.category(t, "Travel", 10000)
	b.category(t, "Fun", 10000)
	b.spend(t, food, 12000, 6)
	b.spend(t, bills, 8500, 6)
	b.spend(t, travel, 9000, 6)

	importID := uuid.New()
	assert.Equal(t, 1, b.check(t, &importID), "one notification for the import")
	inbox := b.inbox(t, b.user)
	require.Len(t, inbox, 1)
	assert.Equal(t, "3 budgets need a look", inbox[0].Title)
	assert.Equal(t, "Food has reached its budget; Travel and Bills are close to their budgets.", inbox[0].Body, "largest spending first")
	assert.Equal(t, "sprout://stats", inbox[0].DeepLink)
	assert.Equal(t, "budget:import:"+importID.String(), inbox[0].DedupKey)

	assert.Zero(t, b.check(t, nil), "the combined alert told each category")

	b.spend(t, bills, 2000, 7)
	single := uuid.New()
	assert.Equal(t, 1, b.check(t, &single), "one category from an import is told on its own")
	assert.Equal(t, "Bills is over budget", b.inbox(t, b.user)[0].Title)
}
