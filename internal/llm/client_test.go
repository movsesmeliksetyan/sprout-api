package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

// categorySchema is the shape of a categorisation answer, with constraints
// structured output does not take.
var categorySchema = llm.MustSchema("category", []byte(`{
	"type": "object",
	"additionalProperties": false,
	"required": ["category_id", "confidence"],
	"properties": {
		"category_id": {"type": ["string", "null"], "maxLength": 36},
		"confidence": {"type": "number", "minimum": 0, "maximum": 1}
	}
}`))

var request = llm.Request{Purpose: "test", System: "You file expenses.", Prompt: "TESCO STORES 3297"}

// fast are options that keep the tests quick.
func fast(logger *slog.Logger) llm.Options {
	return llm.Options{
		TextModel:   "claude-sonnet-5-5",
		VisionModel: "claude-opus-5-5",
		Timeout:     50 * time.Millisecond,
		Backoff:     time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
		Logger:      logger,
	}
}

func newClient(t *testing.T, fake *llm.Fake) (llm.Client, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return llm.New(fake, fast(logger)), &logs
}

func TestCompleteJSON(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	fake.On(request, categorySchema, llm.Reply{
		Text:  `{"category_id": "food", "confidence": 0.82}`,
		Usage: llm.Usage{InputTokens: 1200, OutputTokens: 30},
	})
	client, logs := newClient(t, fake)

	result, err := client.CompleteJSON(context.Background(), request, categorySchema)

	require.NoError(t, err)
	assert.JSONEq(t, `{"category_id": "food", "confidence": 0.82}`, string(result.JSON))
	assert.Equal(t, "claude-sonnet-5-5", result.Model, "the text model answers text")
	assert.Equal(t, llm.Usage{InputTokens: 1200, OutputTokens: 30}, result.Usage)

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
	assert.Equal(t, "llm call", line["msg"])
	assert.Equal(t, "test", line["purpose"])
	assert.Equal(t, "category", line["schema"])
	assert.Equal(t, "claude-sonnet-5-5", line["model"])
	assert.EqualValues(t, 1, line["attempts"])
	assert.EqualValues(t, 1200, line["input_tokens"])
	assert.EqualValues(t, 30, line["output_tokens"])
	assert.InDelta(t, 0.0027, line["cost_usd"], 1e-9, "1,200 in at $2/M and 30 out at $10/M")
	assert.Contains(t, line, "latency_ms")
	assert.NotContains(t, logs.String(), "TESCO", "what was asked is never logged")
}

func TestCompleteJSON_RejectsAnswersThatDoNotMatchTheSchema(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"not JSON":                     `{"category_id": "food",`,
		"prose":                        `I think this is food.`,
		"a field missing":              `{"category_id": "food"}`,
		"a field it does not have":     `{"category_id": "food", "confidence": 0.5, "why": "groceries"}`,
		"the wrong type":               `{"category_id": 7, "confidence": 0.5}`,
		"over a stripped maximum":      `{"category_id": "food", "confidence": 1.5}`,
		"longer than a stripped bound": `{"category_id": "0123456789012345678901234567890123456789", "confidence": 0.5}`,
		"empty":                        ``,
	}
	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := llm.NewFake()
			fake.On(request, categorySchema, llm.Reply{Text: answer})
			client, logs := newClient(t, fake)

			result, err := client.CompleteJSON(context.Background(), request, categorySchema)

			require.ErrorIs(t, err, llm.ErrInvalidOutput)
			assert.Nil(t, result.JSON)
			assert.Equal(t, 1, fake.TotalCalls(), "an answer that does not fit is not asked again")
			assert.Contains(t, logs.String(), `"llm call failed"`)
		})
	}
}

func TestCompleteJSON_RetriesWhatIsWorthRetrying(t *testing.T) {
	t.Parallel()
	ok := llm.Reply{Text: `{"category_id": null, "confidence": 0}`, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}
	status := func(code int) llm.Reply {
		return llm.Reply{Err: &llm.StatusError{StatusCode: code, Err: errors.New("status")}, Usage: llm.Usage{InputTokens: 10}}
	}
	tests := []struct {
		name     string
		replies  []llm.Reply
		want     error
		attempts int
	}{
		{"rate limited, then answered", []llm.Reply{status(429), ok}, nil, 2},
		{"overloaded twice, then answered", []llm.Reply{status(529), status(500), ok}, nil, 3},
		{"a network failure, then answered", []llm.Reply{{Err: errors.New("connection reset")}, ok}, nil, 2},
		{"a slow answer, then a quick one", []llm.Reply{{Delay: time.Second, Text: ok.Text}, ok}, nil, 2},
		{"rate limited every time", []llm.Reply{status(429)}, llm.ErrUnavailable, 3},
		{"timed out every time", []llm.Reply{{Delay: time.Second}}, llm.ErrUnavailable, 3},
		{"a bad request is final", []llm.Reply{status(400), ok}, llm.ErrUnavailable, 1},
		{"a bad key is final", []llm.Reply{status(401), ok}, llm.ErrUnavailable, 1},
		{"a refusal is final", []llm.Reply{{Err: llm.ErrRefused}, ok}, llm.ErrRefused, 1},
		{"a cut-off answer is final", []llm.Reply{{Err: llm.ErrTruncated}, ok}, llm.ErrInvalidOutput, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := llm.NewFake()
			fake.On(request, categorySchema, tt.replies...)
			client, _ := newClient(t, fake)

			result, err := client.CompleteJSON(context.Background(), request, categorySchema)

			if tt.want == nil {
				require.NoError(t, err)
				assert.NotNil(t, result.JSON)
			} else {
				require.ErrorIs(t, err, tt.want)
			}
			assert.Equal(t, tt.attempts, fake.Calls(request, categorySchema))
		})
	}
}

func TestCompleteJSON_AddsUpUsageAcrossAttempts(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	fake.On(request, categorySchema,
		llm.Reply{Err: &llm.StatusError{StatusCode: 503, Err: errors.New("down")}, Usage: llm.Usage{InputTokens: 7}},
		llm.Reply{Text: `{"category_id": null, "confidence": 0}`, Usage: llm.Usage{InputTokens: 100, OutputTokens: 9}},
	)
	client, logs := newClient(t, fake)

	result, err := client.CompleteJSON(context.Background(), request, categorySchema)

	require.NoError(t, err)
	assert.Equal(t, llm.Usage{InputTokens: 107, OutputTokens: 9}, result.Usage)
	assert.Contains(t, logs.String(), `"attempts":2`)
}

func TestCompleteJSON_KeepsToTheWaitTheProviderAsksFor(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	fake.On(request, categorySchema,
		llm.Reply{Err: &llm.StatusError{StatusCode: 429, RetryAfter: 40 * time.Millisecond, Err: errors.New("slow down")}},
		llm.Reply{Text: `{"category_id": null, "confidence": 0}`},
	)
	opts := fast(nil)
	opts.MaxBackoff = time.Second
	client := llm.New(fake, opts)

	start := time.Now()
	_, err := client.CompleteJSON(context.Background(), request, categorySchema)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
}

func TestCompleteJSON_StopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	fake.On(request, categorySchema, llm.Reply{Err: &llm.StatusError{StatusCode: 503, Err: errors.New("down")}})
	opts := fast(nil)
	opts.Backoff, opts.MaxBackoff = time.Second, time.Second
	client := llm.New(fake, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := client.CompleteJSON(ctx, request, categorySchema)

	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, fake.TotalCalls(), "no retry once the caller's deadline has passed")
}

func TestCompleteJSON_UnknownPromptFails(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	client, _ := newClient(t, fake)

	_, err := client.CompleteJSON(context.Background(), request, categorySchema)

	require.ErrorIs(t, err, llm.ErrNoReply)
	assert.ErrorIs(t, err, llm.ErrUnavailable)
	assert.Equal(t, 1, fake.TotalCalls(), "a test's missing reply is not retried")
}

func TestVisionJSON(t *testing.T) {
	t.Parallel()
	fake := llm.NewFake()
	image := llm.Image{MediaType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}}
	fake.OnImage(request, image, categorySchema, llm.Reply{Text: `{"category_id": "food", "confidence": 0.9}`})
	client, _ := newClient(t, fake)

	result, err := client.VisionJSON(context.Background(), request, image, categorySchema)

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", result.Model, "the vision model answers images")
	_, err = client.CompleteJSON(context.Background(), request, categorySchema)
	require.ErrorIs(t, err, llm.ErrNoReply, "the same prompt without the image is another key")

	_, err = client.VisionJSON(context.Background(), request, llm.Image{MediaType: "image/heic", Data: []byte{1}}, categorySchema)
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.Equal(t, 2, fake.TotalCalls(), "an unsupported image is never sent")
}

// counting is a provider that records how many calls are in flight at once.
type counting struct {
	inFlight, peak atomic.Int32
}

func (c *counting) Complete(ctx context.Context, _ llm.Call) (llm.Completion, error) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	case <-time.After(10 * time.Millisecond):
	}
	return llm.Completion{Text: `{"category_id": null, "confidence": 0}`}, nil
}

func TestClient_LimitsCallsInFlight(t *testing.T) {
	t.Parallel()
	provider := &counting{}
	opts := fast(nil)
	opts.MaxConcurrent = 3
	opts.Timeout = time.Second
	client := llm.New(provider, opts)

	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := client.CompleteJSON(context.Background(), request, categorySchema)
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	assert.EqualValues(t, 3, provider.peak.Load(), "never more than three at once, across callers")
}

func TestCostUSD(t *testing.T) {
	t.Parallel()
	cost, ok := llm.CostUSD("claude-opus-5-5", llm.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	assert.True(t, ok)
	assert.InDelta(t, 24.0, cost, 1e-9)
	_, ok = llm.CostUSD("some-other-model", llm.Usage{InputTokens: 1})
	assert.False(t, ok)
}
