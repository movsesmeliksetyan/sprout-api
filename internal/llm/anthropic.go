package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// AnthropicConfig configures the Anthropic provider.
type AnthropicConfig struct {
	APIKey string
	// BaseURL replaces the API's address; empty is the real one.
	BaseURL string
}

// Anthropic is the Provider backed by the Claude API.
type Anthropic struct {
	client anthropic.Client
}

// NewAnthropic returns the Claude API provider. The SDK's own retries are
// off: the client around a provider retries.
func NewAnthropic(cfg AnthropicConfig) *Anthropic {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

// Complete sends one request. The answer is constrained to the call's
// schema, less the keywords structured output does not take. A request the
// model declines is sent on to a fallback model by the API itself.
func (a *Anthropic) Complete(ctx context.Context, call Call) (Completion, error) {
	content := make([]anthropic.BetaContentBlockParamUnion, 0, 2)
	if call.Image != nil {
		content = append(content, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
			Data:      base64.StdEncoding.EncodeToString(call.Image.Data),
			MediaType: anthropic.BetaBase64ImageSourceMediaType(call.Image.MediaType),
		}))
	}
	content = append(content, anthropic.NewBetaTextBlock(call.Prompt))

	params := anthropic.BetaMessageNewParams{
		Model:     call.Model,
		MaxTokens: int64(call.MaxTokens),
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(content...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format: anthropic.BetaJSONOutputFormatParam{Schema: call.Schema.constrained},
			Effort: anthropic.BetaOutputConfigEffort(call.Effort),
		},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	}
	if call.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: call.System}}
	}

	message, err := a.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return Completion{}, &StatusError{
				StatusCode: apiErr.StatusCode,
				RetryAfter: retryAfter(apiErr),
				Err:        err,
			}
		}
		return Completion{}, err
	}

	completion := Completion{
		Model: message.Model,
		Usage: Usage{InputTokens: message.Usage.InputTokens, OutputTokens: message.Usage.OutputTokens},
	}
	switch message.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return completion, fmt.Errorf("%w (%s)", ErrRefused, message.StopDetails.Category)
	case anthropic.BetaStopReasonMaxTokens:
		return completion, ErrTruncated
	}
	var text strings.Builder
	for _, block := range message.Content {
		if block, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(block.Text)
		}
	}
	completion.Text = text.String()
	return completion, nil
}

// retryAfter is the wait the API asked for, in whole seconds; 0 when it
// asked for none.
func retryAfter(apiErr *anthropic.Error) time.Duration {
	if apiErr.Response == nil {
		return 0
	}
	seconds, err := strconv.Atoi(apiErr.Response.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
