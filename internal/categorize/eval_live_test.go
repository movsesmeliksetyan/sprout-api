//go:build live

package categorize_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
	"github.com/movsesmeliksetyan/sprout-api/internal/testutil"
)

// TestEvalLLM is `make eval-llm`: the full pipeline over the labelled set,
// with the configured model for real (LLM_PROVIDER, LLM_API_KEY,
// LLM_MODEL_TEXT). With EVAL_REPORT set it writes the report there.
func TestEvalLLM(t *testing.T) {
	cfg := config.LLM{
		Provider:  os.Getenv("LLM_PROVIDER"),
		APIKey:    config.Secret(os.Getenv("LLM_API_KEY")),
		ModelText: os.Getenv("LLM_MODEL_TEXT"),
	}
	if cfg.Provider == "" || cfg.APIKey == "" || cfg.ModelText == "" {
		t.Skip("LLM_PROVIDER, LLM_API_KEY and LLM_MODEL_TEXT are needed for the live evaluation")
	}
	client, err := llm.NewFromConfig(cfg, llm.Options{Timeout: 2 * time.Minute, MaxAttempts: 5, MaxBackoff: time.Minute, Logger: testutil.Logger(t)})
	require.NoError(t, err)

	rows := loadLabelled(t)
	l := newLedger(t)
	batch := config.MaxCategorizeBatch
	if n, err := strconv.Atoi(os.Getenv("LLM_CATEGORIZE_BATCH")); err == nil {
		batch = n
	}
	l.engine = categorize.NewEngine(l.q, categorize.WithLLM(client, testutil.Logger(t)), categorize.WithLLMBatch(batch))
	deterministic := evaluate(t, newLedger(t), rows, categorize.Options{})
	full := evaluate(t, l, rows, categorize.Options{AllowLLM: true})

	report := fmt.Sprintf("# Categorisation evaluation\n\n"+
		"`make eval-llm` on %s with `%s` (%s). The labelled set is `testdata/categorize/labelled.jsonl`: "+
		"%d rows, synthetic and anonymised, written alongside the seed data, so it measures the pipeline's "+
		"wiring and the seeds' reach more than accuracy on real statements.\n\n"+
		"Targets (PRD §5.5): deterministic coverage ≥ %.0f %% and precision ≥ %.0f %%; full pipeline top-1 accuracy ≥ %.0f %%.\n\n%s\n%s",
		time.Now().Format(time.DateOnly), cfg.ModelText, cfg.Provider, len(rows),
		100*minCoverage, 100*minPrecision, 100*minLLMAccuracy,
		deterministic.report("Deterministic layers (1–4)"), full.report("Full pipeline (1–5)"))
	t.Log("\n" + report)
	if path := os.Getenv("EVAL_REPORT"); path != "" {
		require.NoError(t, os.WriteFile(path, []byte(report), 0o644))
	}
	assert.GreaterOrEqual(t, full.accuracy(), minLLMAccuracy, "top-1 accuracy with the model")
}
