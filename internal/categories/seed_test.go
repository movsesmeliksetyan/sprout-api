package categories_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

var daisy = auth.Claims{Subject: "apple|001234.abcd", Email: "daisy.walker@example.com", Name: "Daisy Walker"}

// seeded is what a default category looks like once stored.
type seeded struct {
	Name, Icon   string
	Shade        int16
	SortOrder    int32
	CategoryType string
}

// contractDefaults is the table in contract §2.3, with each row's type.
var contractDefaults = []seeded{
	{"Food", "utensils", 0, 0, "food"},
	{"Car", "car", 1, 1, "transport"},
	{"Home", "house", 2, 2, "housing"},
	{"Leisure", "bolt", 3, 3, "leisure"},
	{"Self-care", "sparkles", 4, 4, "selfcare"},
	{"Health", "heart", 5, 5, "health"},
	{"Communal", "droplet", 6, 6, "utilities"},
}

func listSeeded(t *testing.T, pool *pgxpool.Pool, user db.User) []seeded {
	t.Helper()
	rows, err := db.New(pool).ListCategories(context.Background(), db.ListCategoriesParams{
		UserID:          user.ID,
		IncludeArchived: true,
	})
	require.NoError(t, err)

	got := make([]seeded, 0, len(rows))
	for _, row := range rows {
		assert.Zero(t, row.MonthlyBudgetMinor, "budgets start at zero")
		assert.Nil(t, row.ArchivedAt)
		require.NotNil(t, row.CategoryType, "every default has a type")
		got = append(got, seeded{row.Name, row.Icon, row.Shade, row.SortOrder, *row.CategoryType})
	}
	return got
}

func TestSeed_NewUserGetsTheDefaults(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	service := users.NewService(pool, users.WithOnUserCreated(categories.Seed))

	user, err := service.Provision(context.Background(), daisy)
	require.NoError(t, err)

	assert.Equal(t, contractDefaults, listSeeded(t, pool, user))
}

func TestSeed_ConcurrentFirstRequests(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	service := users.NewService(pool, users.WithOnUserCreated(categories.Seed))

	const requests = 10
	created := make([]db.User, requests)
	errs := make([]error, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created[i], errs[i] = service.Provision(context.Background(), daisy)
		}()
	}
	close(start)
	wg.Wait()

	for i := range requests {
		require.NoError(t, errs[i])
		assert.Equal(t, created[0].ID, created[i].ID)
	}
	assert.Equal(t, contractDefaults, listSeeded(t, pool, created[0]))
}

func TestSeed_IsIdempotent(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	ctx := context.Background()
	user, err := users.NewService(pool, users.WithOnUserCreated(categories.Seed)).Provision(ctx, daisy)
	require.NoError(t, err)

	require.NoError(t, categories.Seed(ctx, db.New(pool), user))

	assert.Equal(t, contractDefaults, listSeeded(t, pool, user))
}

func TestSeed_IsPerUser(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	ctx := context.Background()
	service := users.NewService(pool, users.WithOnUserCreated(categories.Seed))

	first, err := service.Provision(ctx, daisy)
	require.NoError(t, err)
	second, err := service.Provision(ctx, auth.Claims{Subject: "apple|005678.efgh"})
	require.NoError(t, err)

	assert.Equal(t, contractDefaults, listSeeded(t, pool, first))
	assert.Equal(t, contractDefaults, listSeeded(t, pool, second))
}
