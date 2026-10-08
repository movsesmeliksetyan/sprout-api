package categories_test

import (
	"context"
	"encoding/csv"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categories"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

func TestInferType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, icon string
		want       string // "" means no type
	}{
		{"Groceries", "tag", "food"},
		{"Petrol", "tag", "transport"},
		{"Gym", "tag", "health"},
		{"  eating-OUT ", "tag", "food"},
		{"Self care", "tag", "selfcare"},
		{"Gym membership", "tag", "health"},
		{"Weekend trips", "tag", "travel"},
		// The name wins over the icon.
		{"Rent", "car", "housing"},
		// An unknown name falls back to the icon.
		{"Misc", "paw", "pets"},
		{"Stuff", "plane", "travel"},
		{"Misc", "tag", ""},
		{"Misc", "", ""},
		{"", "", ""},
		{"Caterpillar", "tag", ""},
	}
	for _, tt := range tests {
		got := categories.InferType(tt.name, tt.icon)
		if tt.want == "" {
			assert.Nil(t, got, "%q with icon %q", tt.name, tt.icon)
			continue
		}
		if assert.NotNil(t, got, "%q with icon %q", tt.name, tt.icon) {
			assert.Equal(t, tt.want, *got, "%q with icon %q", tt.name, tt.icon)
		}
	}
}

// The data file may only name types the database has and icons the contract
// has.
func TestTypeHints_MatchTheSchemaAndContract(t *testing.T) {
	t.Parallel()
	pool := testutil.NewDB(t)
	stored, err := db.New(pool).ListCategoryTypes(context.Background())
	require.NoError(t, err)
	known := map[string]bool{}
	for _, categoryType := range stored {
		known[categoryType.Key] = true
	}

	file, err := os.Open("data/types.csv")
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	records, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)

	for _, record := range records[1:] {
		match, value, categoryType := record[0], record[1], record[2]
		assert.True(t, known[categoryType], "%s %q has unknown type %q", match, value, categoryType)
		if match == "icon" {
			assert.True(t, httpx.CategoryIcon(value).Valid(), "unknown icon %q", value)
		}
	}
}
