package money

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		minor    int64
		currency string
		want     string
	}{
		// The figures used in the design.
		{184250, "USD", "$1,842.50"},
		{21000, "USD", "$210"},
		{40855, "USD", "$408.55"},
		{1000, "USD", "$10"},
		{15000, "USD", "$150"},
		{9000, "USD", "$90"},

		{0, "USD", "$0"},
		{1, "USD", "$0.01"},
		{5, "USD", "$0.05"},
		{50, "USD", "$0.50"},
		{99, "USD", "$0.99"},
		{100, "USD", "$1"},
		{101, "USD", "$1.01"},
		{99999, "USD", "$999.99"},
		{100000, "USD", "$1,000"},
		{123456789012, "USD", "$1,234,567,890.12"},
		{-1000, "USD", "-$10"},
		{-2440, "USD", "-$24.40"},
		{-5, "USD", "-$0.05"},
		{math.MaxInt64, "USD", "$92,233,720,368,547,758.07"},
		{math.MinInt64, "USD", "-$92,233,720,368,547,758.08"},

		{2440, "GBP", "£24.40"},
		{2440, "EUR", "€24.40"},
		{250000, "CAD", "CA$2,500"},
		{250000, "AUD", "A$2,500"},
		{1500000, "AMD", "֏15,000"},
		{2440, "usd", "$24.40"},

		// No minor unit.
		{1500, "JPY", "¥1,500"},
		{0, "JPY", "¥0"},
		{-7, "JPY", "-¥7"},

		// No known symbol: the code, and two decimals.
		{1250, "CHF", "CHF 12.50"},
		{-1250, "CHF", "-CHF 12.50"},
		{300000, "sek", "SEK 3,000"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, Format(tt.minor, tt.currency))
		})
	}
}

func TestDivRound(t *testing.T) {
	tests := []struct {
		numerator, denominator, want int64
	}{
		{0, 5, 0},
		{10, 5, 2},
		{7, 2, 4},   // 3.5
		{5, 2, 3},   // 2.5
		{1, 2, 1},   // 0.5
		{1, 3, 0},   // 0.33
		{2, 3, 1},   // 0.67
		{-7, 2, -4}, // -3.5 rounds away from zero
		{-1, 2, -1},
		{-1, 3, 0},
		{-2, 3, -1},
		{7, -2, -4},
		{-7, -2, 4},
		{600000, 52, 11538}, // 11538.46
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, DivRound(tt.numerator, tt.denominator), "%d / %d", tt.numerator, tt.denominator)
	}

	assert.Panics(t, func() { _ = DivRound(1, 0) })
}

func TestPercent(t *testing.T) {
	tests := []struct {
		name        string
		part, total int64
		want        int
		wantOK      bool
	}{
		{"food budget from the design", 48250, 50000, 97, true}, // 96.5
		{"goals total from the design", 133800, 216900, 62, true},
		{"over budget", 16000, 15000, 107, true},
		{"nothing spent", 0, 50000, 0, true},
		{"exactly all", 50000, 50000, 100, true},
		{"rounds down below half", 1, 300, 0, true}, // 0.33
		{"rounds up at half", 1, 200, 1, true},      // 0.5
		{"zero total", 500, 0, 0, false},
		{"zero of zero", 0, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Percent(tt.part, tt.total)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPercentChange(t *testing.T) {
	tests := []struct {
		name              string
		current, previous int64
		want              int
		wantOK            bool
	}{
		{"month total from the design", 184250, 205250, -10, true}, // -10.23
		{"down 8 percent", 46000, 50000, -8, true},
		{"doubled", 2000, 1000, 100, true},
		{"unchanged", 1000, 1000, 0, true},
		{"dropped to nothing", 0, 1000, -100, true},
		{"negative half rounds away from zero", 199, 200, -1, true}, // -0.5
		{"no previous spend", 1000, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PercentChange(tt.current, tt.previous)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShares(t *testing.T) {
	tests := []struct {
		name    string
		amounts []int64
		want    []int
	}{
		{"empty", nil, []int{}},
		{"single", []int64{4200}, []int{100}},
		{"even split", []int64{50, 50}, []int{50, 50}},
		{"exact percentages", []int64{2500, 7500}, []int{25, 75}},
		{"thirds: the spare point goes to the first", []int64{1, 1, 1}, []int{34, 33, 33}},
		{"sixths: spare points go in order", []int64{1, 1, 1, 1, 1, 1}, []int{17, 17, 17, 17, 16, 16}},
		// 49.5 / 49.5 / 1: plain rounding would give 50+50+1 = 101.
		{"plain rounding would overshoot", []int64{495, 495, 10}, []int{50, 49, 1}},
		// 33.4 / 33.3 / 33.3: plain rounding would give 99.
		{"plain rounding would undershoot", []int64{334, 333, 333}, []int{34, 33, 33}},
		// 36.36 / 27.27 / 27.27 / 9.09: floors sum to 99, and .36 is the largest remainder.
		{"largest remainder wins the spare point", []int64{4, 3, 3, 1}, []int{37, 27, 27, 9}},
		{"zero amounts get nothing", []int64{0, 300, 0, 100}, []int{0, 75, 0, 25}},
		{"all zero", []int64{0, 0, 0}, []int{0, 0, 0}},
		{"negative counts as zero", []int64{-500, 100, 300}, []int{0, 25, 75}},
		{"tiny next to huge", []int64{1, 1_000_000_000}, []int{0, 100}},
		{
			"seven categories from the design",
			[]int64{48250, 31000, 42000, 21000, 16000, 14000, 12000},
			[]int{26, 17, 23, 11, 9, 8, 6},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Shares(tt.amounts)

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShares_AlwaysSumTo100AndStayNearExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))

	for range 2000 {
		amounts := make([]int64, 1+rng.IntN(12))
		var total int64
		for i := range amounts {
			if rng.IntN(5) > 0 {
				amounts[i] = rng.Int64N(5_000_000)
			}
			total += amounts[i]
		}
		if total == 0 {
			continue
		}

		shares := Shares(amounts)

		sum := 0
		for i, share := range shares {
			sum += share
			exact := float64(amounts[i]) * 100 / float64(total)
			require.InDelta(t, exact, share, 1, "amounts %v: share %d is %d, exact %.3f", amounts, i, share, exact)
		}
		require.Equal(t, 100, sum, "amounts %v gave %v", amounts, shares)
	}
}

func TestShares_DoesNotModifyItsInput(t *testing.T) {
	amounts := []int64{3, 1, 2}

	Shares(amounts)

	assert.Equal(t, []int64{3, 1, 2}, amounts)
}
