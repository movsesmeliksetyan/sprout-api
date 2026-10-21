package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// groqBaseURL is the address of Groq's OpenAI-compatible API.
const groqBaseURL = "https://api.groq.com/openai/v1"

// maxGroqErrorBody bounds how much of an error answer is read.
const maxGroqErrorBody = 64 << 10

// GroqConfig configures the Groq provider.
type GroqConfig struct {
	APIKey string
	// BaseURL replaces the API's address; empty is the real one.
	BaseURL string
	// HTTPClient sends the requests; nil is a client without a timeout of
	// its own, as each attempt has one.
	HTTPClient *http.Client
}

// Groq is the Provider backed by Groq's chat completions API. Groq has no
// Go SDK, so this speaks its OpenAI-compatible HTTP API directly.
type Groq struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewGroq returns the Groq provider.
func NewGroq(cfg GroqConfig) *Groq {
	g := &Groq{apiKey: cfg.APIKey, baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: cfg.HTTPClient}
	if g.baseURL == "" {
		g.baseURL = groqBaseURL
	}
	if g.http == nil {
		g.http = &http.Client{}
	}
	return g
}

type groqRequest struct {
	Model          string             `json:"model"`
	Messages       []groqMessage      `json:"messages"`
	MaxTokens      int                `json:"max_completion_tokens"`
	ResponseFormat groqResponseFormat `json:"response_format"`
	// ReasoningFormat must not be "raw" with a JSON response format.
	ReasoningFormat string `json:"reasoning_format"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type groqMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type groqPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *groqImageURL `json:"image_url,omitempty"`
}

type groqImageURL struct {
	URL string `json:"url"`
}

type groqResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema groqJSONSchema `json:"json_schema"`
}

type groqJSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type groqResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

type groqError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Complete sends one request. The answer is constrained to the call's
// schema in strict mode, less the keywords strict mode does not take; the
// model's reasoning, if any, is not returned.
func (g *Groq) Complete(ctx context.Context, call Call) (Completion, error) {
	var user any = call.Prompt
	if call.Image != nil {
		user = []groqPart{
			{Type: "image_url", ImageURL: &groqImageURL{
				URL: "data:" + call.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(call.Image.Data),
			}},
			{Type: "text", Text: call.Prompt},
		}
	}
	messages := make([]groqMessage, 0, 2)
	if call.System != "" {
		messages = append(messages, groqMessage{Role: "system", Content: call.System})
	}
	messages = append(messages, groqMessage{Role: "user", Content: user})

	body, err := json.Marshal(groqRequest{
		Model:     call.Model,
		Messages:  messages,
		MaxTokens: call.MaxTokens,
		ResponseFormat: groqResponseFormat{
			Type: "json_schema",
			JSONSchema: groqJSONSchema{
				Name:   schemaName(call.Schema.Name),
				Strict: true,
				Schema: call.Schema.constrained,
			},
		},
		ReasoningFormat: "hidden",
		ReasoningEffort: string(call.Effort),
	})
	if err != nil {
		return Completion{}, fmt.Errorf("llm: groq request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Completion{}, fmt.Errorf("llm: groq request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.http.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Completion{}, groqStatusError(resp)
	}
	var answer groqResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return Completion{}, fmt.Errorf("llm: groq answer: %w", err)
	}
	completion := Completion{
		Model: answer.Model,
		Usage: Usage{InputTokens: answer.Usage.PromptTokens, OutputTokens: answer.Usage.CompletionTokens},
	}
	if len(answer.Choices) == 0 {
		return completion, fmt.Errorf("%w: no choices in the answer", ErrInvalidOutput)
	}
	choice := answer.Choices[0]
	if choice.FinishReason == "length" {
		return completion, ErrTruncated
	}
	completion.Text = choice.Message.Content
	return completion, nil
}

// groqStatusError turns an error answer into a *StatusError. An answer the
// API itself could not make match the schema (json_validate_failed) is
// ErrGenerationFailed: it happens now and then on prompts that succeed when
// asked again, so it is retried, and is invalid output once attempts run
// out.
func groqStatusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxGroqErrorBody))
	var body groqError
	_ = json.Unmarshal(raw, &body)
	message := body.Error.Message
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	err := fmt.Errorf("groq %s: %s", body.Error.Code, message)
	if body.Error.Code == "json_validate_failed" {
		return fmt.Errorf("%w: %w", ErrGenerationFailed, err)
	}
	return &StatusError{StatusCode: resp.StatusCode, RetryAfter: retryAfterSeconds(resp.Header), Err: err}
}

// retryAfterSeconds is the wait a Retry-After header asks for, in whole
// seconds; 0 when it asks for none.
func retryAfterSeconds(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// schemaName is a schema's name as Groq accepts one: letters, digits,
// underscores and dashes.
func schemaName(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
	if clean == "" {
		return "answer"
	}
	return clean
}
