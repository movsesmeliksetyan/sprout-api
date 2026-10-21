package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/config"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

func chatCompletion(finishReason, content string) string {
	body, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "model": "qwen/qwen3.8-27b",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": finishReason,
			"message": map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 2400, "completion_tokens": 40, "total_tokens": 2440},
	})
	return string(body)
}

func groqCall() llm.Call {
	c := call()
	c.Model = "qwen/qwen3.8-27b"
	c.Effort = llm.EffortNone
	return c
}

func TestGroq_Request(t *testing.T) {
	t.Parallel()
	s := &stub{status: http.StatusOK, body: chatCompletion("stop", `{"category_id":"food","confidence":0.8}`)}
	var auth, path string
	url := s.serve(t)
	provider := llm.NewGroq(llm.GroqConfig{APIKey: "gsk-test", BaseURL: url, HTTPClient: &http.Client{
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			auth, path = r.Header.Get("Authorization"), r.URL.Path
			return http.DefaultTransport.RoundTrip(r)
		}),
	}})

	completion, err := provider.Complete(context.Background(), groqCall())

	require.NoError(t, err)
	assert.Equal(t, `{"category_id":"food","confidence":0.8}`, completion.Text)
	assert.Equal(t, "qwen/qwen3.8-27b", completion.Model)
	assert.Equal(t, llm.Usage{InputTokens: 2400, OutputTokens: 40}, completion.Usage)
	assert.Equal(t, "Bearer gsk-test", auth)
	assert.Equal(t, "/chat/completions", path)

	req := s.request
	assert.Equal(t, "qwen/qwen3.8-27b", req["model"])
	assert.EqualValues(t, 1024, req["max_completion_tokens"])
	assert.Equal(t, "hidden", req["reasoning_format"], "raw reasoning is refused with a JSON answer")
	assert.Equal(t, "none", req["reasoning_effort"])

	messages := req["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, map[string]any{"role": "system", "content": "You file expenses."}, messages[0])
	user := messages[1].(map[string]any)
	assert.Equal(t, "user", user["role"])
	parts := user["content"].([]any)
	require.Len(t, parts, 2)
	assert.Equal(t, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,cG5nIQ=="}}, parts[0])
	assert.Equal(t, map[string]any{"type": "text", "text": "TESCO STORES 3297"}, parts[1])

	format := req["response_format"].(map[string]any)
	assert.Equal(t, "json_schema", format["type"])
	jsonSchema := format["json_schema"].(map[string]any)
	assert.Equal(t, "category", jsonSchema["name"])
	assert.Equal(t, true, jsonSchema["strict"])
	properties := jsonSchema["schema"].(map[string]any)["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "number"}, properties["confidence"], "minimum and maximum are left out")
}

func TestGroq_TextOnly(t *testing.T) {
	t.Parallel()
	s := &stub{status: http.StatusOK, body: chatCompletion("stop", `{}`)}
	provider := llm.NewGroq(llm.GroqConfig{APIKey: "gsk-test", BaseURL: s.serve(t)})
	c := groqCall()
	c.Image, c.System, c.Effort = nil, "", ""

	_, err := provider.Complete(context.Background(), c)

	require.NoError(t, err)
	messages := s.request["messages"].([]any)
	assert.Equal(t, []any{map[string]any{"role": "user", "content": "TESCO STORES 3297"}}, messages,
		"no system message without a system prompt; plain text content")
	assert.NotContains(t, s.request, "reasoning_effort", "the model's default")
}

func TestGroq_Failures(t *testing.T) {
	t.Parallel()
	t.Run("cut off", func(t *testing.T) {
		t.Parallel()
		s := &stub{status: http.StatusOK, body: chatCompletion("length", `{"category_id":`)}
		completion, err := llm.NewGroq(llm.GroqConfig{BaseURL: s.serve(t)}).Complete(context.Background(), groqCall())
		require.ErrorIs(t, err, llm.ErrTruncated)
		assert.EqualValues(t, 2400, completion.Usage.InputTokens, "a failed answer was still paid for")
	})
	t.Run("rate limited", func(t *testing.T) {
		t.Parallel()
		s := &stub{
			status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"3"}},
			body:   `{"error":{"message":"Rate limit reached","type":"tokens","code":"rate_limit_exceeded"}}`,
		}
		_, err := llm.NewGroq(llm.GroqConfig{BaseURL: s.serve(t)}).Complete(context.Background(), groqCall())
		var status *llm.StatusError
		require.ErrorAs(t, err, &status)
		assert.Equal(t, http.StatusTooManyRequests, status.StatusCode)
		assert.Equal(t, 3*time.Second, status.RetryAfter)
		assert.Contains(t, err.Error(), "Rate limit reached")
	})
	t.Run("the answer failed the schema", func(t *testing.T) {
		t.Parallel()
		s := &stub{
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Generated JSON does not match the expected schema","type":"invalid_request_error","code":"json_validate_failed"}}`,
		}
		_, err := llm.NewGroq(llm.GroqConfig{BaseURL: s.serve(t)}).Complete(context.Background(), groqCall())
		require.ErrorIs(t, err, llm.ErrInvalidOutput)
		require.ErrorIs(t, err, llm.ErrGenerationFailed)
	})
	t.Run("server error without a body", func(t *testing.T) {
		t.Parallel()
		s := &stub{status: http.StatusBadGateway, body: ``}
		_, err := llm.NewGroq(llm.GroqConfig{BaseURL: s.serve(t)}).Complete(context.Background(), groqCall())
		var status *llm.StatusError
		require.ErrorAs(t, err, &status)
		assert.Equal(t, http.StatusBadGateway, status.StatusCode)
		assert.Contains(t, err.Error(), "Bad Gateway")
	})
}

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{config.LLMProviderGroq, config.LLMProviderAnthropic} {
		client, err := llm.NewFromConfig(config.LLM{Provider: provider, ModelText: "m", ModelVision: "m"}, llm.Options{})
		require.NoError(t, err, provider)
		assert.NotNil(t, client)
	}
	_, err := llm.NewFromConfig(config.LLM{Provider: "openai"}, llm.Options{})
	require.Error(t, err)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
