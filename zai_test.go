package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weave-agent/weave/sdk"
	"github.com/weave-agent/weave/sdk/model"
	"github.com/weave-agent/weave/sdk/retry"
	"github.com/weave-agent/weave/utils/openaicompat"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func newTestProvider(server *httptest.Server, model string) sdk.Provider {
	if model == "" {
		model = "glm-5.1"
	}

	return &provider{
		client: server.Client(),
		retry:  retry.DefaultConfig(),
		config: openaicompat.ProviderConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Model:   model,
		},
	}
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

func TestProviderInit_WithCustomHTTPAndRetryConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "test-key")

	maxRetries := 2
	multiplier := 1.5
	cfg := stubConfig{
		providers: map[string]map[string]any{
			"zai": {
				"model":    "glm-custom",
				"base_url": "https://example.test/api",
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

	assert.Equal(t, 2, p.retry.MaxRetries)
	assert.Equal(t, 250*time.Millisecond, p.retry.BaseDelay)
	assert.Equal(t, 5*time.Second, p.retry.MaxDelay)
	assert.InDelta(t, 1.5, p.retry.Multiplier, 0.0001)
	assert.Equal(t, retry.JitterNone, p.retry.Jitter)

	assert.Equal(t, "https://example.test/api", p.config.BaseURL)
	assert.Equal(t, "test-key", p.config.APIKey)
	assert.Equal(t, "glm-custom", p.config.Model)
	assert.Equal(t, true, p.config.ExtraBody["tool_stream"])

	body := map[string]any{"reasoning_effort": "high"}
	p.config.ModifyRequest(body, &model.StreamOptions{ThinkingLevel: model.ThinkingLow})
	assert.Equal(t, true, body["enable_thinking"])
	assert.NotContains(t, body, "reasoning_effort")
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
