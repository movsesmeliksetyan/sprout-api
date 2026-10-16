package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

// stub is a stand-in for the Claude API that answers every request with
// one status and body, and keeps the last request.
type stub struct {
	status  int
	header  http.Header
	body    string
	calls   atomic.Int32
	request map[string]any
	beta    string
}

func (s *stub) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.NoError(t, json.Unmarshal(raw, &s.request))
		s.beta = r.Header.Get("anthropic-beta")
		for key, values := range s.header {
			w.Header()[key] = values
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func message(stopReason, text string) string {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"stop_reason": stopReason, "stop_sequence": nil,
		"stop_details": map[string]any{"type": "refusal", "category": "cyber", "explanation": "declined"},
		"content":      []any{map[string]any{"type": "text", "text": text}},
		"usage":        map[string]any{"input_tokens": 321, "output_tokens": 12},
	})
	return string(body)
}

func call() llm.Call {
	return llm.Call{
		Model:     "claude-sonnet-5-5",
		System:    "You file expenses.",
		Prompt:    "TESCO STORES 3297",
		Image:     &llm.Image{MediaType: "image/png", Data: []byte("png!")},
		Schema:    categorySchema,
		MaxTokens: 1024,
		Effort:    llm.EffortLow,
	}
}

func TestAnthropic_Request(t *testing.T) {
	t.Parallel()
	s := &stub{status: http.StatusOK, body: message("end_turn", `{"category_id":"food","confidence":0.8}`)}
	provider := llm.NewAnthropic(llm.AnthropicConfig{APIKey: "test-key", BaseURL: s.serve(t)})

	completion, err := provider.Complete(context.Background(), call())

	require.NoError(t, err)
	assert.Equal(t, `{"category_id":"food","confidence":0.8}`, completion.Text)
	assert.Equal(t, "claude-sonnet-5-5", completion.Model)
	assert.Equal(t, llm.Usage{InputTokens: 321, OutputTokens: 12}, completion.Usage)

	req := s.request
	assert.Equal(t, "claude-sonnet-5-5", req["model"])
	assert.EqualValues(t, 1024, req["max_tokens"])
	assert.Equal(t, "default", req["fallbacks"], "a declined request goes to the API's fallback model")
	assert.Contains(t, s.beta, "server-side-fallback-2026-07-01")
	assert.NotContains(t, req, "thinking", "the model's own default")

	system := req["system"].([]any)
	assert.Equal(t, "You file expenses.", system[0].(map[string]any)["text"])
	content := req["messages"].([]any)[0].(map[string]any)["content"].([]any)
	require.Len(t, content, 2)
	image := content[0].(map[string]any)
	assert.Equal(t, "image", image["type"])
	assert.Equal(t, map[string]any{"type": "base64", "media_type": "image/png", "data": "cG5nIQ=="}, image["source"])
	assert.Equal(t, map[string]any{"type": "text", "text": "TESCO STORES 3297"}, content[1])

	output := req["output_config"].(map[string]any)
	assert.Equal(t, "low", output["effort"])
	format := output["format"].(map[string]any)
	assert.Equal(t, "json_schema", format["type"])
	properties := format["schema"].(map[string]any)["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "number"}, properties["confidence"],
		"minimum and maximum are left out of what is sent")
	assert.Equal(t, map[string]any{"type": []any{"string", "null"}}, properties["category_id"])
	assert.Equal(t, false, format["schema"].(map[string]any)["additionalProperties"])
}

func TestAnthropic_StopReasons(t *testing.T) {
	t.Parallel()
	for stopReason, want := range map[string]error{"refusal": llm.ErrRefused, "max_tokens": llm.ErrTruncated} {
		s := &stub{status: http.StatusOK, body: message(stopReason, `{"category_id":`)}
		provider := llm.NewAnthropic(llm.AnthropicConfig{APIKey: "test-key", BaseURL: s.serve(t)})

		completion, err := provider.Complete(context.Background(), call())

		require.ErrorIs(t, err, want, stopReason)
		assert.EqualValues(t, 321, completion.Usage.InputTokens, "a failed answer was still paid for")
	}
}

func TestAnthropic_ErrorsCarryTheStatus(t *testing.T) {
	t.Parallel()
	s := &stub{
		status: http.StatusTooManyRequests,
		header: http.Header{"Retry-After": {"7"}},
		body:   `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
	}
	provider := llm.NewAnthropic(llm.AnthropicConfig{APIKey: "test-key", BaseURL: s.serve(t)})

	_, err := provider.Complete(context.Background(), call())

	var status *llm.StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusTooManyRequests, status.StatusCode)
	assert.Equal(t, 7*time.Second, status.RetryAfter)
	assert.EqualValues(t, 1, s.calls.Load(), "the SDK does not retry on its own; the client does")
}

func TestAnthropic_KeepsPropertiesNamedLikeKeywords(t *testing.T) {
	t.Parallel()
	schema := llm.MustSchema("keywords", []byte(`{
		"type": "object",
		"additionalProperties": false,
		"required": ["pattern", "maximum"],
		"properties": {
			"pattern": {"type": "string", "pattern": "^[a-z]+$"},
			"maximum": {"type": "integer", "maximum": 10}
		}
	}`))
	s := &stub{status: http.StatusOK, body: message("end_turn", `{}`)}
	provider := llm.NewAnthropic(llm.AnthropicConfig{APIKey: "test-key", BaseURL: s.serve(t)})
	c := call()
	c.Schema = schema

	_, err := provider.Complete(context.Background(), c)

	require.NoError(t, err)
	sent := s.request["output_config"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
	assert.Equal(t, map[string]any{
		"pattern": map[string]any{"type": "string"},
		"maximum": map[string]any{"type": "integer"},
	}, sent["properties"])
	assert.Equal(t, []any{"pattern", "maximum"}, sent["required"])
}

func TestNewSchema_RefusesWhatDoesNotCompile(t *testing.T) {
	t.Parallel()
	_, err := llm.NewSchema("broken", []byte(`{"type": "object",`))
	require.Error(t, err)
	_, err = llm.NewSchema("bad type", []byte(`{"type": "thing"}`))
	require.Error(t, err)
}
