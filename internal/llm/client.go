package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Client asks a language model for JSON that matches a schema. Callers
// treat every error as "no answer" and carry on without one: an LLM failure
// never fails the work that asked.
type Client interface {
	// CompleteJSON answers a text prompt.
	CompleteJSON(ctx context.Context, req Request, schema *Schema) (Result, error)
	// VisionJSON answers a prompt about an image.
	VisionJSON(ctx context.Context, req Request, image Image, schema *Schema) (Result, error)
}

// Errors a Client returns, wrapped. Anything else is a failure to get an
// answer at all.
var (
	// ErrInvalidOutput is an answer that is not JSON matching the schema,
	// or that was cut off.
	ErrInvalidOutput = errors.New("llm: answer does not match the schema")
	// ErrRefused is the model declining to answer.
	ErrRefused = errors.New("llm: the model declined to answer")
	// ErrUnavailable is no answer after every attempt, or a request the
	// provider will not take.
	ErrUnavailable = errors.New("llm: no answer from the provider")
)

// Effort is how much thought the model puts into an answer.
type Effort string

// The effort levels. The zero value leaves it to the model; EffortNone
// asks for an answer without reasoning first, where the model allows it.
const (
	EffortNone   Effort = "none"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Request is what to ask. Prompt and System must already be redacted (see
// Redact): the client sends them as they are.
type Request struct {
	// Purpose names the call in logs, such as "categorize" or "mapping".
	Purpose string
	System  string
	Prompt  string
	// MaxTokens bounds the answer; 0 is DefaultMaxTokens.
	MaxTokens int
	Effort    Effort
}

// DefaultMaxTokens bounds an answer when a request does not.
const DefaultMaxTokens = 8192

// Image is a picture sent with a VisionJSON request.
type Image struct {
	// MediaType is image/jpeg, image/png, image/gif or image/webp.
	MediaType string
	Data      []byte
}

// Result is an answer that matches the schema.
type Result struct {
	JSON json.RawMessage
	// Model is the model that answered, which can be a fallback.
	Model string
	Usage Usage
}

// Usage is what one call consumed.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Provider makes one attempt at a call to a model. The client around it
// retries, times out, limits concurrency and checks answers.
type Provider interface {
	Complete(ctx context.Context, call Call) (Completion, error)
}

// Call is one request as a provider receives it.
type Call struct {
	Model     string
	System    string
	Prompt    string
	Image     *Image
	Schema    *Schema
	MaxTokens int
	Effort    Effort
}

// Completion is a provider's answer, not yet checked.
type Completion struct {
	Text  string
	Model string
	Usage Usage
}

// StatusError is a provider's refusal of a request, by HTTP status.
type StatusError struct {
	StatusCode int
	// RetryAfter is how long the provider asked to wait; 0 when it did not.
	RetryAfter time.Duration
	Err        error
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("llm: provider answered %d: %v", e.StatusCode, e.Err)
}

func (e *StatusError) Unwrap() error { return e.Err }

// ErrTruncated is a provider's answer cut off at the token limit.
var ErrTruncated = fmt.Errorf("%w: the answer was cut off", ErrInvalidOutput)

// ErrGenerationFailed is a provider that could not produce an answer
// matching the schema this time. It is invalid output if it persists, but
// unlike an answer that does not fit it is worth another attempt.
var ErrGenerationFailed = fmt.Errorf("%w: the provider failed to generate it", ErrInvalidOutput)

// Options configure a client. Zero values take the defaults.
type Options struct {
	// TextModel answers CompleteJSON and VisionModel VisionJSON.
	TextModel   string
	VisionModel string
	// Timeout bounds one attempt (default 60 s).
	Timeout time.Duration
	// MaxAttempts is how often a call is tried (default 3).
	MaxAttempts int
	// Backoff is the wait before the second attempt, doubled each time
	// after (default 1 s), up to MaxBackoff (default 15 s). A wait the
	// provider asks for is kept to, up to MaxBackoff.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// MaxConcurrent is how many calls run at once, across every caller of
	// the client (default 4).
	MaxConcurrent int
	Logger        *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	if o.Backoff == 0 {
		o.Backoff = time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 15 * time.Second
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = 4
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return o
}

// New returns a Client that sends calls to provider.
func New(provider Provider, opts Options) Client {
	opts = opts.withDefaults()
	return &client{provider: provider, opts: opts, slots: make(chan struct{}, opts.MaxConcurrent)}
}

type client struct {
	provider Provider
	opts     Options
	// slots holds a token for every call in flight.
	slots chan struct{}
}

func (c *client) CompleteJSON(ctx context.Context, req Request, schema *Schema) (Result, error) {
	return c.call(ctx, req, c.opts.TextModel, nil, schema)
}

func (c *client) VisionJSON(ctx context.Context, req Request, image Image, schema *Schema) (Result, error) {
	switch image.MediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return Result{}, fmt.Errorf("%w: images of type %q are not supported", ErrUnavailable, image.MediaType)
	}
	return c.call(ctx, req, c.opts.VisionModel, &image, schema)
}

func (c *client) call(ctx context.Context, req Request, model string, image *Image, schema *Schema) (Result, error) {
	call := Call{
		Model:     model,
		System:    req.System,
		Prompt:    req.Prompt,
		Image:     image,
		Schema:    schema,
		MaxTokens: req.MaxTokens,
		Effort:    req.Effort,
	}
	if call.MaxTokens == 0 {
		call.MaxTokens = DefaultMaxTokens
	}

	start := time.Now()
	var usage Usage
	attempts := 0
	completion, err := func() (Completion, error) {
		for {
			attempts++
			completion, err := c.attempt(ctx, call)
			usage.InputTokens += completion.Usage.InputTokens
			usage.OutputTokens += completion.Usage.OutputTokens
			if err == nil {
				return completion, nil
			}
			wait, retry := c.retryable(ctx, err, attempts)
			if !retry {
				return completion, err
			}
			select {
			case <-ctx.Done():
				return completion, ctx.Err()
			case <-time.After(wait):
			}
		}
	}()

	result := Result{Model: completion.Model, Usage: usage}
	if result.Model == "" {
		result.Model = model
	}
	if err == nil {
		err = schema.validate([]byte(completion.Text))
	}
	if err == nil {
		result.JSON = json.RawMessage(completion.Text)
	} else if !errors.Is(err, ErrInvalidOutput) && !errors.Is(err, ErrRefused) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	c.log(ctx, req, schema, result, attempts, time.Since(start), err)
	if err != nil {
		return Result{Model: result.Model, Usage: usage}, err
	}
	return result, nil
}

// attempt makes one try at call, holding a slot while it runs.
func (c *client) attempt(ctx context.Context, call Call) (Completion, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return Completion{}, ctx.Err()
	}
	defer func() { <-c.slots }()

	attemptCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	completion, err := c.provider.Complete(attemptCtx, call)
	if err != nil && ctx.Err() == nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("llm: no answer within %s: %w", c.opts.Timeout, err)
	}
	return completion, err
}

// retryable reports whether a failed attempt is worth another, and after
// how long. Refusals, answers that do not fit, and requests the provider
// will not take are final; a provider failing to generate an answer is not.
func (c *client) retryable(ctx context.Context, err error, attempts int) (time.Duration, bool) {
	if ctx.Err() != nil || attempts >= c.opts.MaxAttempts {
		return 0, false
	}
	final := errors.Is(err, ErrInvalidOutput) || errors.Is(err, ErrRefused) || errors.Is(err, ErrNoReply)
	if final && !errors.Is(err, ErrGenerationFailed) {
		return 0, false
	}
	var asked time.Duration
	var status *StatusError
	if errors.As(err, &status) {
		switch code := status.StatusCode; {
		case code == 408, code == 409, code == 429, code >= 500:
			asked = status.RetryAfter
		default:
			return 0, false
		}
	}

	backoff := c.opts.Backoff << (attempts - 1)
	if backoff <= 0 || backoff > c.opts.MaxBackoff {
		backoff = c.opts.MaxBackoff
	}
	// Half the backoff, then a random part of the other half, so callers
	// that failed together do not come back together.
	wait := backoff/2 + rand.N(backoff/2+1)
	return min(max(wait, asked), c.opts.MaxBackoff), true
}

func (c *client) log(ctx context.Context, req Request, schema *Schema, result Result, attempts int, latency time.Duration, err error) {
	attrs := []slog.Attr{
		slog.String("purpose", req.Purpose),
		slog.String("schema", schema.Name),
		slog.String("model", result.Model),
		slog.Int("attempts", attempts),
		slog.Int64("latency_ms", latency.Milliseconds()),
		slog.Int64("input_tokens", result.Usage.InputTokens),
		slog.Int64("output_tokens", result.Usage.OutputTokens),
	}
	if cost, ok := CostUSD(result.Model, result.Usage); ok {
		attrs = append(attrs, slog.Float64("cost_usd", cost))
	}
	if err == nil {
		c.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "llm call", attrs...)
		return
	}
	attrs = append(attrs, slog.String("error", err.Error()))
	c.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "llm call failed", attrs...)
}

// price is what a model charges, in billionths of a dollar per token.
type price struct{ input, output int64 }

// prices are the providers' list prices for the models in use. A model
// that is not here has its tokens logged without a cost.
var prices = map[string]price{
	"qwen/qwen3.8-27b":  {input: 800, output: 4000},
	"claude-opus-5-5":   {input: 4000, output: 20000},
	"claude-sonnet-5-5": {input: 2000, output: 10000},
	"claude-sonnet-5":   {input: 2000, output: 10000},
	"claude-haiku-5-5":  {input: 100, output: 500},
}

// CostUSD is what usage costs on model at list price, and whether the
// price of model is known.
func CostUSD(model string, usage Usage) (float64, bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	nanos := usage.InputTokens*p.input + usage.OutputTokens*p.output
	return float64(nanos) / 1e9, true
}
