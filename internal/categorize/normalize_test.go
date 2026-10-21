package categorize_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
)

type normalizeCase struct {
	line        int
	description string
	want        categorize.Merchant
}

// normalizeCases reads testdata/categorize/normalize.tsv.
func normalizeCases(t testing.TB) []normalizeCase {
	t.Helper()
	file, err := os.Open("../../testdata/categorize/normalize.tsv")
	require.NoError(t, err)
	defer file.Close()

	var cases []normalizeCase
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		columns := strings.Split(text, "\t")
		require.Len(t, columns, 3, "line %d: want description, key and display", line)
		cases = append(cases, normalizeCase{
			line:        line,
			description: columns[0],
			want:        categorize.Merchant{Key: columns[1], Display: columns[2]},
		})
	}
	require.NoError(t, scanner.Err())
	return cases
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := normalizeCases(t)
	require.GreaterOrEqual(t, len(cases), 200)

	for _, c := range cases {
		assert.Equal(t, c.want, categorize.Normalize(c.description), "line %d: %q", c.line, c.description)
	}
}

func TestNormalize_IsIdempotent(t *testing.T) {
	t.Parallel()
	for _, c := range normalizeCases(t) {
		once := categorize.Normalize(c.description)
		assert.Equal(t, once, categorize.Normalize(once.Display), "line %d: %q", c.line, c.description)
	}
}

func TestNormalize_KeyForm(t *testing.T) {
	t.Parallel()
	for _, c := range normalizeCases(t) {
		key := categorize.Normalize(c.description).Key
		assert.Equal(t, strings.ToLower(key), key, "line %d", c.line)
		assert.Equal(t, strings.Join(strings.Fields(key), " "), key, "line %d: one space between words", c.line)
		assert.NotContainsf(t, key, ".", "line %d", c.line)
	}
}

func TestNormalize_SpellingsOfOneMerchantShareAKey(t *testing.T) {
	t.Parallel()
	groups := map[string][]string{
		"tesco": {
			"CARD PAYMENT TO TESCO STORES 3297 LONDON GB ON 14-07", "TESCO EXPRESS 4412", "Tesco Metro", "tesco",
		},
		"amazon": {
			"AMZN MKTP UK*2K3XX4 AMAZON.CO.UK", "WWW.AMAZON.CO.UK", "AMAZON.COM", "Amazon",
		},
		"glovo": {"Ք: GLOVO APP YEREVAN AM 165817", "GLOVOAPP", "Glovo"},
	}
	for want, descriptions := range groups {
		for _, description := range descriptions {
			assert.Equal(t, want, categorize.Normalize(description).Key, "%q", description)
		}
	}
}

func TestNormalize_Empty(t *testing.T) {
	t.Parallel()
	for _, description := range []string{"", "   ", "\t\n", "**** **** **** 1234", "14/07/2025 17:24"} {
		assert.Equal(t, categorize.Merchant{}, categorize.Normalize(description), "%q", description)
	}
}

func BenchmarkNormalize(b *testing.B) {
	cases := normalizeCases(b)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		categorize.Normalize(cases[i%len(cases)].description)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "descriptions/s")
}

// TestNormalize_IsFastEnough holds the normaliser to 100,000 descriptions a
// second, the figure statement import is planned around.
func TestNormalize_IsFastEnough(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing test: run without -short and -race")
	}
	result := testing.Benchmark(BenchmarkNormalize)
	perSecond := float64(result.N) / result.T.Seconds()
	t.Logf("%.0f descriptions/s (%s per description)", perSecond, result.T/time.Duration(result.N))
	assert.GreaterOrEqual(t, perSecond, 100_000.0)
}
