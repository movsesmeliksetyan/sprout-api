package llm

import (
	"fmt"

	"github.com/movsesmeliksetyan/sprout-api/internal/config"
)

// NewFromConfig returns a client for the configured provider and models,
// with opts for everything else. Groq is the provider for v1.
func NewFromConfig(cfg config.LLM, opts Options) (Client, error) {
	var provider Provider
	switch cfg.Provider {
	case config.LLMProviderGroq:
		provider = NewGroq(GroqConfig{APIKey: string(cfg.APIKey)})
	case config.LLMProviderAnthropic:
		provider = NewAnthropic(AnthropicConfig{APIKey: string(cfg.APIKey)})
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
	opts.TextModel, opts.VisionModel = cfg.ModelText, cfg.ModelVision
	return New(provider, opts), nil
}
