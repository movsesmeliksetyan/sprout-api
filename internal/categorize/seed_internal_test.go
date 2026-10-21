package categorize

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSeeds_Embedded(t *testing.T) {
	t.Parallel()
	s, err := loadSeeds(seedData)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(s.dictionary), 300, "merchants")
	assert.GreaterOrEqual(t, len(s.keywords), 60, "keyword patterns")

	for _, merchant := range s.dictionary {
		assert.GreaterOrEqual(t, len(merchant.key), 2, "%q", merchant.display)
	}
	mcc := make(map[string]string, len(s.mcc))
	for _, code := range s.mcc {
		mcc[code.mcc] = code.categoryType
	}
	assert.Equal(t, "food", mcc["5411"])
	assert.Equal(t, "food", mcc["5812"])
	assert.Equal(t, "transport", mcc["5542"])
	assert.Equal(t, "travel", mcc["3350"])
	assert.Equal(t, "health", mcc["5912"])
	assert.NotContains(t, mcc, "6011", "cash machines have no type")
}

func TestLoadSeeds_Rejects(t *testing.T) {
	t.Parallel()
	const (
		dictionary = "merchant_key,display_name,category_type\ntesco,Tesco,food\n"
		mcc        = "from,to,category_type,description\n5411,5411,food,Groceries\n"
		keywords   = "pattern,category_type,priority\npharmac*,health,30\n"
	)
	files := func(d, m, k string) fstest.MapFS {
		return fstest.MapFS{
			"data/dictionary.csv": {Data: []byte(d)},
			"data/mcc.csv":        {Data: []byte(m)},
			"data/keywords.csv":   {Data: []byte(k)},
		}
	}
	_, err := loadSeeds(files(dictionary, mcc, keywords))
	require.NoError(t, err)

	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"key that is not the normalised name", files(dictionary+"tesco express,Tesco Express,food\n", mcc, keywords), `normalises to "tesco"`},
		{"merchant listed twice", files(dictionary+"tesco,TESCO,food\n", mcc, keywords), "listed twice"},
		{"merchant without a type", files(dictionary+"lidl,Lidl,\n", mcc, keywords), "no category type"},
		{"wrong header", files("key,display,type\n", mcc, keywords), "header"},
		{"wrong number of columns", files(dictionary+"lidl,Lidl\n", mcc, keywords), "wrong number of fields"},
		{"code that is not four digits", files(dictionary, mcc+"541,541,food,x\n", keywords), "four-digit"},
		{"range that runs backwards", files(dictionary, mcc+"5500,5499,food,x\n", keywords), "backwards"},
		{"overlapping ranges", files(dictionary, mcc+"5400,5420,food,x\n", keywords), "two ranges"},
		{"pattern not in key form", files(dictionary, mcc, keywords+"Pharmacy,health,30\n"), "key form"},
		{"pattern with an inner star", files(dictionary, mcc, keywords+"ph*rm,health,30\n"), "key form"},
		{"pattern listed twice", files(dictionary, mcc, keywords+"pharmac*,food,10\n"), "listed twice"},
		{"priority that is not a number", files(dictionary, mcc, keywords+"gym,selfcare,high\n"), "not a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadSeeds(tt.files)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
