//go:build live

package llm_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// TestLive_CompleteJSON calls the configured provider for real:
// go test -tags=live ./internal/llm with LLM_PROVIDER, LLM_API_KEY and
// LLM_MODEL_TEXT set. It costs a fraction of a cent.
func TestLive_CompleteJSON(t *testing.T) {
	cfg := config.LLM{
		Provider:  os.Getenv("LLM_PROVIDER"),
		APIKey:    config.Secret(os.Getenv("LLM_API_KEY")),
		ModelText: os.Getenv("LLM_MODEL_TEXT"),
	}
	if cfg.Provider == "" || cfg.APIKey == "" || cfg.ModelText == "" {
		t.Skip("LLM_PROVIDER, LLM_API_KEY and LLM_MODEL_TEXT are needed for the live test")
	}
	client, err := llm.NewFromConfig(cfg, llm.Options{Timeout: 2 * time.Minute, Logger: testutil.Logger(t)})
	require.NoError(t, err)
	schema := llm.MustSchema("live_category", []byte(`{
		"type": "object",
		"additionalProperties": false,
		"required": ["category", "confidence"],
		"properties": {
			"category": {"type": "string", "enum": ["groceries", "fuel", "restaurants", "other"]},
			"confidence": {"type": "number", "minimum": 0, "maximum": 1}
		}
	}`))

	result, err := client.CompleteJSON(context.Background(), llm.Request{
		Purpose:   "live_smoke",
		System:    "You file card payments from a bank statement under one category.",
		Prompt:    "Description: " + llm.Redact("CARD PAYMENT TO TESCO STORES 3297 LONDON GB 4111111111111111"),
		MaxTokens: 2048,
		Effort:    llm.EffortLow,
	}, schema)

	require.NoError(t, err)
	var answer struct {
		Category   string  `json:"category"`
		Confidence float64 `json:"confidence"`
	}
	require.NoError(t, json.Unmarshal(result.JSON, &answer))
	assert.Equal(t, "groceries", answer.Category)
	assert.Positive(t, result.Usage.InputTokens)
	t.Logf("%s answered %s (%d in, %d out)", result.Model, result.JSON, result.Usage.InputTokens, result.Usage.OutputTokens)
}
