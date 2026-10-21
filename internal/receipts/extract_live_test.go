//go:build live

package receipts_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/receipts"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// TestExtractLive reads every receipt image in testdata/receipts with the
// configured vision model for real (LLM_PROVIDER, LLM_API_KEY,
// LLM_MODEL_VISION) and checks it against its expected.json. Run it with
// make eval-receipts.
func TestExtractLive(t *testing.T) {
	cfg := config.LLM{
		Provider:    os.Getenv("LLM_PROVIDER"),
		APIKey:      config.Secret(os.Getenv("LLM_API_KEY")),
		ModelVision: os.Getenv("LLM_MODEL_VISION"),
	}
	if cfg.Provider == "" || cfg.APIKey == "" || cfg.ModelVision == "" {
		t.Skip("LLM_PROVIDER, LLM_API_KEY and LLM_MODEL_VISION are needed for the live receipt test")
	}
	client, err := llm.NewFromConfig(cfg, llm.Options{Timeout: time.Minute, MaxAttempts: 3, Logger: testutil.Logger(t)})
	require.NoError(t, err)

	files, err := filepath.Glob("../../testdata/receipts/*.expected.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".expected.json")
		t.Run(name, func(t *testing.T) {
			var want struct {
				MerchantContains string `json:"merchant_contains"`
				TotalMinor       int64  `json:"total_minor"`
				Currency         string `json:"currency"`
				Date             string `json:"date"`
				Last4            string `json:"last4"`
				LineItems        int    `json:"line_items"`
			}
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &want))
			photo, err := os.ReadFile(filepath.Join(filepath.Dir(path), name+".png"))
			require.NoError(t, err)

			img, err := receipts.NormalizeImage(photo)
			require.NoError(t, err)
			got, err := receipts.Extract(context.Background(), client, nil, uuid.Nil, img, want.Currency, time.UTC)
			require.NoError(t, err)
			require.True(t, got.IsReceipt)
			require.NotNil(t, got.TotalMinor)
			assert.Equal(t, want.TotalMinor, *got.TotalMinor, "total")
			if assert.NotNil(t, got.Currency) {
				assert.Equal(t, want.Currency, *got.Currency)
			}
			if assert.NotNil(t, got.Merchant) {
				assert.Contains(t, strings.ToLower(*got.Merchant), want.MerchantContains)
			}
			if assert.NotNil(t, got.PurchasedAt) {
				assert.Equal(t, want.Date, got.PurchasedAt.Format(time.DateOnly))
			}
			if assert.NotNil(t, got.PaymentMethod) {
				assert.True(t, strings.HasSuffix(*got.PaymentMethod, want.Last4), *got.PaymentMethod)
			}
			assert.Len(t, got.LineItems, want.LineItems)
		})
	}
}
