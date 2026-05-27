package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weave-agent/weave/sdk"
	"github.com/weave-agent/weave/sdk/model"
	"github.com/weave-agent/weave/sdk/retry"
	"github.com/weave-agent/weave/settings"
	"github.com/weave-agent/weave/utils/openaicompat"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const countBaseURLSetting = "tokenizer_" + "base_url"

type stubConfig struct {
	providers map[string]map[string]any
	sdk.NoopConfig
}

func (s stubConfig) ExtensionConfig(scope, name string, target any) error {
	if scope != "providers" {
		return fmt.Errorf("unexpected scope %q", scope)
	}

	section, ok := s.providers[name]
	if !ok {
		return nil
	}

	data, err := json.Marshal(section)
	if err != nil {
		return fmt.Errorf("marshal stub config: %w", err)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal stub config: %w", err)
	}

	return nil
}

func newTestProvider(server *httptest.Server, modelName string) *provider {
	if modelName == "" {
		modelName = "glm-5.1"
	}

	retryConfig := retry.DefaultConfig()

	return &provider{
		client:           server.Client(),
		tokenizerBaseURL: server.URL,
		config: openaicompat.ProviderConfig{
			BaseURL:     server.URL,
			APIKey:      "test-key",
			Model:       modelName,
			RetryConfig: &retryConfig,
			ExtraBody: map[string]any{
				"tool_stream": true,
			},
			ModifyRequest: func(body map[string]any, so *model.StreamOptions) {
				if so.ThinkingLevel != model.ThinkingOff {
					body["enable_thinking"] = true
					delete(body, "reasoning_effort")
				}
			},
		},
	}
}

func loadTestFullConfig(t *testing.T, body string) *settings.FullConfig {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".weave"), 0o750))

	projectDir := t.TempDir()
	settingsDir := filepath.Join(projectDir, ".weave")
	require.NoError(t, os.MkdirAll(settingsDir, 0o750))

	settingsPath := filepath.Join(settingsDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(body), 0o600))

	cfg, err := settings.LoadFullConfig(settingsPath)
	require.NoError(t, err)
	cfg.SetProjectDir(projectDir)

	return cfg
}

type headerRoundTripper struct {
	base http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Zai-Test-Transport", "configured")

	resp, err := h.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("round trip test request: %w", err)
	}

	return resp, nil
}

func collectEvents(t *testing.T, ch <-chan sdk.ProviderEvent) []sdk.ProviderEvent {
	t.Helper()

	var events []sdk.ProviderEvent

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}

			events = append(events, evt)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for events")
		}
	}
}

func sseChunk(delta openaicompat.ChunkDelta, finish *string) string {
	chunk := openaicompat.StreamChunk{
		ID: "chatcmpl-test",
		Choices: []struct {
			Index        int                     `json:"index"`
			Delta        openaicompat.ChunkDelta `json:"delta"`
			FinishReason *string                 `json:"finish_reason"`
		}{
			{Index: 0, Delta: delta, FinishReason: finish},
		},
	}
	data, _ := json.Marshal(chunk)

	return "data: " + string(data) + "\n"
}

func sseDone() string {
	return "data: [DONE]\n"
}

func sseStream(events ...string) string {
	return strings.Join(events, "") + "\n"
}

func setupServer(response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(w, response)
	}))
}

func TestStream_TextResponse(t *testing.T) {
	stream := sseStream(
		sseChunk(openaicompat.ChunkDelta{Role: "assistant"}, nil),
		sseChunk(openaicompat.ChunkDelta{Content: "Hello!"}, nil),
		sseChunk(openaicompat.ChunkDelta{}, new("stop")),
		sseDone(),
	)

	server := setupServer(stream)
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)

	events := collectEvents(t, ch)

	var textParts []string

	for _, e := range events {
		if e.Type == sdk.ProviderEventTextDelta {
			textParts = append(textParts, e.Content.(string))
		}
	}

	assert.Equal(t, []string{"Hello!"}, textParts)
}

func TestStream_ToolCall(t *testing.T) {
	stream := sseStream(
		sseChunk(openaicompat.ChunkDelta{Role: "assistant"}, nil),
		sseChunk(openaicompat.ChunkDelta{
			ToolCalls: []openaicompat.ToolCallDelta{
				{Index: 0, ID: "call_abc", Type: "function", Function: &openaicompat.FunctionCallDelta{Name: "bash"}},
			},
		}, nil),
		sseChunk(openaicompat.ChunkDelta{
			ToolCalls: []openaicompat.ToolCallDelta{
				{Index: 0, Function: &openaicompat.FunctionCallDelta{Arguments: `{"command":"ls"}`}},
			},
		}, nil),
		sseChunk(openaicompat.ChunkDelta{}, new("tool_calls")),
		sseDone(),
	)

	server := setupServer(stream)
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("run ls")},
	})
	require.NoError(t, err)

	events := collectEvents(t, ch)

	var toolCalls []sdk.ToolCall

	for _, e := range events {
		if e.Type == sdk.ProviderEventToolCall {
			toolCalls = append(toolCalls, e.Content.(sdk.ToolCall))
		}
	}

	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_abc", toolCalls[0].ID)
	assert.Equal(t, "bash", toolCalls[0].Name)
	assert.Equal(t, map[string]any{"command": "ls"}, toolCalls[0].Arguments)
}

func TestStream_WithSystemPrompt(t *testing.T) {
	var receivedBody openaicompat.ChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		SystemPrompt: "You are helpful.",
		Messages:     []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	require.NotEmpty(t, receivedBody.Messages)
	assert.Equal(t, "system", receivedBody.Messages[0].Role)
	assert.Equal(t, "You are helpful.", receivedBody.Messages[0].Content)
}

func TestStream_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	_, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid API key")
}

func TestStream_UsesConfiguredRetryConfig(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++

		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	retryConfig := retry.Config{
		MaxRetries: 0,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   1 * time.Millisecond,
		Multiplier: 1,
		Jitter:     retry.JitterNone,
	}
	p.config.RetryConfig = &retryConfig

	_, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	assert.Equal(t, 1, attempts)
}

func TestStream_UsesConfiguredHTTPClient(t *testing.T) {
	var receivedTransportHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTransportHeader = r.Header.Get("X-Zai-Test-Transport")

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	baseClient := server.Client()
	p := newTestProvider(server, "glm-5.1")
	p.client = &http.Client{
		Transport: headerRoundTripper{base: baseClient.Transport},
	}

	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	assert.Equal(t, "configured", receivedTransportHeader)
}

func TestStream_WithTools(t *testing.T) {
	var receivedBody openaicompat.ChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
		Tools: []sdk.ToolDef{
			{
				Name:        "bash",
				Description: "Run a command",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	require.Len(t, receivedBody.Tools, 1)
	assert.Equal(t, "bash", receivedBody.Tools[0].Function.Name)
}

func TestStream_SendsToolStreamExtraBody(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	assert.Equal(t, true, receivedBody["tool_stream"])
}

func TestStream_UsageEventMapping(t *testing.T) {
	stream := sseStream(
		sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
		`data: {"id":"chatcmpl-test","choices":[],"usage":{"prompt_tokens":17,"completion_tokens":5}}`+"\n",
		sseDone(),
	)

	server := setupServer(stream)
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)

	events := collectEvents(t, ch)

	var usages []sdk.ProviderUsage

	for _, e := range events {
		if e.Type == sdk.ProviderEventUsage {
			usages = append(usages, e.Content.(sdk.ProviderUsage))
		}
	}

	require.Len(t, usages, 1)
	assert.Equal(t, 17, usages[0].InputTokens)
	assert.Equal(t, 5, usages[0].OutputTokens)
	assert.Zero(t, usages[0].CacheReadTokens)
}

func TestStream_CachedTokenUsageDetail(t *testing.T) {
	stream := sseStream(
		sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
		`data: {"id":"chatcmpl-test","choices":[],"usage":{"prompt_tokens":24,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":19}}}`+"\n",
		sseDone(),
	)

	server := setupServer(stream)
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)

	events := collectEvents(t, ch)

	var usages []sdk.ProviderUsage

	for _, e := range events {
		if e.Type == sdk.ProviderEventUsage {
			usages = append(usages, e.Content.(sdk.ProviderUsage))
		}
	}

	require.Len(t, usages, 1)
	assert.Equal(t, 24, usages[0].InputTokens)
	assert.Equal(t, 6, usages[0].OutputTokens)
	assert.Equal(t, 19, usages[0].CacheReadTokens)
}

func TestCountTokens_UsesTokenizerEndpoint(t *testing.T) {
	var receivedPath string

	var receivedAuth string

	var receivedBody map[string]any

	var decodeErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		decodeErr = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":42,"total_tokens":42}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	count, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		SystemPrompt: "You are helpful.",
		Messages:     []sdk.Message{sdk.NewUserMessage("hi")},
		Tools: []sdk.ToolDef{
			{
				Name:        "bash",
				Description: "Run a command",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
	}, model.WithModel("glm-custom"))
	require.NoError(t, err)

	require.NoError(t, decodeErr)
	assert.Equal(t, "/tokenizer", receivedPath)
	assert.Equal(t, "Bearer test-key", receivedAuth)
	assert.Equal(t, "glm-custom", receivedBody["model"])
	assert.NotContains(t, receivedBody, "tool_stream")
	require.IsType(t, []any{}, receivedBody["messages"])
	messages := receivedBody["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"])
	assert.Equal(t, "user", messages[1].(map[string]any)["role"])
	require.IsType(t, []any{}, receivedBody["tools"])
	assert.Equal(t, 42, count.InputTokens)
	assert.Zero(t, count.OutputTokens)
	assert.Equal(t, sdk.TokenCountSourceTokenizer, count.Source)
	assert.InDelta(t, 0.95, count.Confidence, 0.0001)
}

func TestCountTokens_UsesConfiguredTokenizerBaseURL(t *testing.T) {
	var chatRequests int

	var tokenizerRequests int

	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatRequests++

		http.NotFound(w, r)
	}))
	defer chatServer.Close()

	tokenizerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenizerRequests++

		assert.Equal(t, "/tokenizer", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":31,"total_tokens":31}}`)
	}))
	defer tokenizerServer.Close()

	p := &provider{
		client:           tokenizerServer.Client(),
		tokenizerBaseURL: tokenizerServer.URL,
		config: openaicompat.ProviderConfig{
			BaseURL: chatServer.URL,
			APIKey:  "test-key",
			Model:   "glm-5.1",
		},
	}

	count, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)

	assert.Equal(t, 31, count.InputTokens)
	assert.Zero(t, chatRequests)
	assert.Equal(t, 1, tokenizerRequests)
}

func TestCountTokens_ConvertsToolMessagesForTokenizer(t *testing.T) {
	var receivedBody map[string]any

	var decodeErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeErr = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":55,"total_tokens":55}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	count, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{
			sdk.NewUserMessage("run ls"),
			{
				Role: sdk.RoleAssistant,
				ToolCalls: []sdk.ToolCall{
					{
						ID:        "call_1",
						Name:      "bash",
						Arguments: map[string]any{"command": "ls"},
					},
				},
			},
			sdk.NewToolResultMessage("call_1", "bash", "file.txt", false),
		},
	})
	require.NoError(t, err)

	require.NoError(t, decodeErr)
	require.IsType(t, []any{}, receivedBody["messages"])
	messages := receivedBody["messages"].([]any)
	require.Len(t, messages, 3)
	assert.Equal(t, "user", messages[0].(map[string]any)["role"])
	assert.Equal(t, "assistant", messages[1].(map[string]any)["role"])
	assert.Equal(t, "user", messages[2].(map[string]any)["role"])
	assert.Equal(t, 55, count.InputTokens)
}

func TestCountTokens_ReturnsErrorWhenPromptTokensMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"total_tokens":17}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	_, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing prompt_tokens")
}

func TestCountTokens_ReturnsErrorWhenTokenizerResponseIsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	_, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse tokenizer response")
}

func TestCountTokens_ReturnsErrorWhenTokenizerCountIsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":0,"total_tokens":0}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	_, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing prompt_tokens")
}

func TestCountTokens_ReturnsTokenizerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"model not supported","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	_, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model not supported")
}

func TestCountTokens_AppliesThinkingRequestModification(t *testing.T) {
	var receivedBody map[string]any

	var decodeErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeErr = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":12,"total_tokens":12}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	p.config.ModifyRequest = func(body map[string]any, so *model.StreamOptions) {
		body["reasoning_effort"] = "high"
		if so.ThinkingLevel != model.ThinkingOff {
			body["enable_thinking"] = true
			delete(body, "reasoning_effort")
		}
	}

	count, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	}, model.WithThinkingLevel(model.ThinkingLow))
	require.NoError(t, err)

	require.NoError(t, decodeErr)
	assert.Equal(t, 12, count.InputTokens)
	assert.Equal(t, true, receivedBody["enable_thinking"])
	assert.NotContains(t, receivedBody, "reasoning_effort")
}

func TestCountTokens_UsesConfiguredRetryConfig(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++

		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":21,"total_tokens":21}}`)
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	retryConfig := retry.Config{
		MaxRetries: 1,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   1 * time.Millisecond,
		Multiplier: 1,
		Jitter:     retry.JitterNone,
	}
	p.config.RetryConfig = &retryConfig

	count, err := p.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)

	assert.Equal(t, 21, count.InputTokens)
	assert.Equal(t, 2, attempts)
}

func TestCountTokens_RespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	retryConfig := retry.Config{
		MaxRetries: 5,
		BaseDelay:  time.Hour,
		MaxDelay:   time.Hour,
		Multiplier: 1,
		Jitter:     retry.JitterNone,
	}
	p.config.RetryConfig = &retryConfig

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.CountTokens(ctx, sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestStream_MultipleToolCalls(t *testing.T) {
	stream := sseStream(
		sseChunk(openaicompat.ChunkDelta{Role: "assistant"}, nil),
		sseChunk(openaicompat.ChunkDelta{
			ToolCalls: []openaicompat.ToolCallDelta{
				{Index: 0, ID: "call_1", Type: "function", Function: &openaicompat.FunctionCallDelta{Name: "bash"}},
				{Index: 1, ID: "call_2", Type: "function", Function: &openaicompat.FunctionCallDelta{Name: "read"}},
			},
		}, nil),
		sseChunk(openaicompat.ChunkDelta{
			ToolCalls: []openaicompat.ToolCallDelta{
				{Index: 0, Function: &openaicompat.FunctionCallDelta{Arguments: `{"command":"ls"}`}},
				{Index: 1, Function: &openaicompat.FunctionCallDelta{Arguments: `{"path":"/tmp/file"}`}},
			},
		}, nil),
		sseChunk(openaicompat.ChunkDelta{}, new("tool_calls")),
		sseDone(),
	)

	server := setupServer(stream)
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("do stuff")},
	})
	require.NoError(t, err)

	events := collectEvents(t, ch)

	var toolCalls []sdk.ToolCall

	for _, e := range events {
		if e.Type == sdk.ProviderEventToolCall {
			toolCalls = append(toolCalls, e.Content.(sdk.ToolCall))
		}
	}

	require.Len(t, toolCalls, 2)
	names := []string{toolCalls[0].Name, toolCalls[1].Name}
	assert.Contains(t, names, "bash")
	assert.Contains(t, names, "read")
}

func TestStream_DefaultModel(t *testing.T) {
	var receivedBody openaicompat.ChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	assert.Equal(t, "glm-5.1", receivedBody.Model)
}

func TestStream_SendsCorrectBaseURL(t *testing.T) {
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	p := newTestProvider(server, "glm-5.1")
	ch, err := p.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.NoError(t, err)
	collectEvents(t, ch)

	assert.Equal(t, "/chat/completions", receivedPath)
}

func TestRegister(t *testing.T) {
	assert.True(t, sdk.ProviderRegistered("zai"))
}

func TestGLM51ModelMetadata(t *testing.T) {
	m, ok := model.GetModel("glm-5.1")
	require.True(t, ok)

	assert.Equal(t, providerName, m.Provider)
	assert.Equal(t, "GLM-5.1", m.DisplayName)
	assert.True(t, m.Reasoning)
	assert.Equal(t, 204800, m.ContextWindow)
	assert.Equal(t, 131072, m.MaxTokens)
}

func TestDefaultModelRegistration(t *testing.T) {
	m, ok := model.DefaultModelForProvider(providerName)
	require.True(t, ok)

	assert.Equal(t, "glm-5.1", m.ID)
	assert.Equal(t, "GLM-5.1", m.DisplayName)
	assert.True(t, m.Default)
	assert.Equal(t, 204800, m.ContextWindow)
	assert.Equal(t, 131072, m.MaxTokens)
}

func TestDefaultModelReasoningCapabilities(t *testing.T) {
	m, ok := model.DefaultModelForProvider(providerName)
	require.True(t, ok)

	assert.True(t, m.Reasoning)
	assert.False(t, m.SupportsXHigh)
	assert.Equal(t, model.ThinkingHigh, model.ClampForModel(model.ThinkingXHigh, m))
}

func TestProviderInit_DefaultConfigWorks(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	cfg := loadTestFullConfig(t, `{}`)

	got, err := sdk.GetProvider("zai", cfg)
	require.NoError(t, err)

	p, ok := got.(*provider)
	require.True(t, ok)
	require.NotNil(t, p.client)
	assert.NotNil(t, p.client.Transport)
	require.NotNil(t, p.config.RetryConfig)
	assert.Equal(t, retry.Config{
		MaxRetries: 5,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
		Multiplier: 2,
		Jitter:     retry.JitterFull,
	}, *p.config.RetryConfig)
	assert.Equal(t, "https://api.z.ai/api/coding/paas/v4", p.config.BaseURL)
	assert.Equal(t, "https://api.z.ai/api/paas/v4", p.tokenizerBaseURL)
	assert.Equal(t, "test-key", p.config.APIKey)
	assert.Equal(t, "glm-5.1", p.config.Model)
	assert.Equal(t, true, p.config.ExtraBody["tool_stream"])
}

func TestProviderInit_WithCustomHTTPAndRetryConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "test-key")

	maxRetries := 2
	multiplier := 1.5
	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":             "glm-custom",
				"base_url":          "https://example.test/api",
				countBaseURLSetting: "https://tokenizer.example.test/api",
				"http": map[string]any{
					"tls_handshake_timeout":   "1500ms",
					"response_header_timeout": "2s",
					"idle_conn_timeout":       "3s",
				},
				"retry": map[string]any{
					"max_retries": &maxRetries,
					"base_delay":  "250ms",
					"max_delay":   "5s",
					"multiplier":  &multiplier,
					"jitter":      "none",
				},
			},
		},
	}

	got, err := sdk.GetProvider("zai", cfg)
	require.NoError(t, err)

	p, ok := got.(*provider)
	require.True(t, ok)
	require.NotNil(t, p.client)

	transport, ok := p.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 1500*time.Millisecond, transport.TLSHandshakeTimeout)
	assert.Equal(t, 2*time.Second, transport.ResponseHeaderTimeout)
	assert.Equal(t, 3*time.Second, transport.IdleConnTimeout)

	require.NotNil(t, p.config.RetryConfig)
	assert.Equal(t, 2, p.config.RetryConfig.MaxRetries)
	assert.Equal(t, 250*time.Millisecond, p.config.RetryConfig.BaseDelay)
	assert.Equal(t, 5*time.Second, p.config.RetryConfig.MaxDelay)
	assert.InDelta(t, 1.5, p.config.RetryConfig.Multiplier, 0.0001)
	assert.Equal(t, retry.JitterNone, p.config.RetryConfig.Jitter)

	assert.Equal(t, "https://example.test/api", p.config.BaseURL)
	assert.Equal(t, "https://tokenizer.example.test/api", p.tokenizerBaseURL)
	assert.Equal(t, "test-key", p.config.APIKey)
	assert.Equal(t, "glm-custom", p.config.Model)
	assert.Equal(t, true, p.config.ExtraBody["tool_stream"])

	body := map[string]any{"reasoning_effort": "high"}
	p.config.ModifyRequest(body, &model.StreamOptions{ThinkingLevel: model.ThinkingLow})
	assert.Equal(t, true, body["enable_thinking"])
	assert.NotContains(t, body, "reasoning_effort")
}

func TestProviderInit_CustomRetryConfigUsedByStream(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++

		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseStream(
			sseChunk(openaicompat.ChunkDelta{Content: "ok"}, nil),
			sseChunk(openaicompat.ChunkDelta{}, new("stop")),
			sseDone(),
		))
	}))
	defer server.Close()

	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":             "glm-5.1",
				"base_url":          server.URL,
				countBaseURLSetting: server.URL,
				"retry": map[string]any{
					"max_retries": 0,
					"base_delay":  "1ms",
					"max_delay":   "1ms",
					"multiplier":  1,
					"jitter":      "none",
				},
			},
		},
	}

	got, err := sdk.GetProvider("zai", cfg)
	require.NoError(t, err)

	_, err = got.Stream(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	assert.Equal(t, 1, attempts)
}

func TestProviderInit_CustomRetryConfigUsedByCountTokens(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++

		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer server.Close()

	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":             "glm-5.1",
				"base_url":          server.URL,
				countBaseURLSetting: server.URL,
				"retry": map[string]any{
					"max_retries": 0,
					"base_delay":  "1ms",
					"max_delay":   "1ms",
					"multiplier":  1,
					"jitter":      "none",
				},
			},
		},
	}

	got, err := sdk.GetProvider("zai", cfg)
	require.NoError(t, err)

	counter, ok := got.(sdk.TokenCounter)
	require.True(t, ok)

	_, err = counter.CountTokens(context.Background(), sdk.ProviderRequest{
		Messages: []sdk.Message{sdk.NewUserMessage("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	assert.Equal(t, 1, attempts)
}

func TestProviderInit_InvalidHTTPConfigFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "test-key")

	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":    "glm-custom",
				"base_url": "https://example.test/api",
				"http": map[string]any{
					"response_header_timeout": "not-a-duration",
				},
			},
		},
	}

	_, err := sdk.GetProvider("zai", cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zai: resolve HTTP config")
	assert.Contains(t, err.Error(), "invalid response_header_timeout")
}

func TestProviderInit_InvalidRetryConfigFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "test-key")

	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":    "glm-custom",
				"base_url": "https://example.test/api",
				"retry": map[string]any{
					"jitter": "sideways",
				},
			},
		},
	}

	_, err := sdk.GetProvider("zai", cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zai: resolve retry config")
	assert.Contains(t, err.Error(), "invalid jitter")
}
