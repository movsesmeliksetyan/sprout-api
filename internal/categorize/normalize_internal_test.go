package categorize

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// files is a complete, valid set of data files with aliases as given.
func files(aliases string) fstest.MapFS {
	empty := &fstest.MapFile{Data: []byte("# nothing\n")}
	return fstest.MapFS{
		"data/prefixes.txt":   {Data: []byte("POS\n")},
		"data/processors.txt": empty,
		"data/countries.txt":  {Data: []byte("GB US\n")},
		"data/regions.txt":    empty,
		"data/cities.txt":     {Data: []byte("LONDON\nNEW YORK\n")},
		"data/suffixes.txt":   empty,
		"data/legal.txt":      {Data: []byte("LTD\n")},
		"data/aliases.tsv":    {Data: []byte(aliases)},
	}
}

func TestLoadNormalizer(t *testing.T) {
	t.Parallel()
	n, err := loadNormalizer(files("tesco\tTesco\tTESCO STORES|TESCO EXPRESS\n"))
	require.NoError(t, err)
	assert.Equal(t, Merchant{Key: "tesco", Display: "Tesco"}, n.normalize("POS TESCO EXPRESS 12 LONDON GB"))
	assert.Equal(t, Merchant{Key: "tesco", Display: "Tesco"}, n.normalize("tesco"), "the display name is a spelling")
	assert.Equal(t, 2, n.maxCityWords)
}

func TestLoadNormalizer_RefusesBadData(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"a spelling of two merchants": "tesco\tTesco\tTESCO STORES\ntesco metro\tTesco Metro\tTESCO STORES\n",
		"a missing column":            "tesco\tTesco\n",
		"a key not in key form":       "Tesco.com\tTesco\tTESCO STORES\n",
	}
	for name, aliases := range tests {
		_, err := loadNormalizer(files(aliases))
		assert.Error(t, err, name)
	}
	broken := files("")
	delete(broken, "data/cities.txt")
	_, err := loadNormalizer(broken)
	assert.Error(t, err, "a missing file")
}

func TestEmbeddedDataLoads(t *testing.T) {
	t.Parallel()
	n, err := loadNormalizer(data)
	require.NoError(t, err)
	assert.NotEmpty(t, n.prefixes)
	assert.NotEmpty(t, n.aliases)
	for i := 1; i < len(n.prefixes); i++ {
		assert.GreaterOrEqual(t, len(n.prefixes[i-1]), len(n.prefixes[i]), "longest prefix first")
	}
}
