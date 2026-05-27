package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/weave-agent/weave/sdk"
	"github.com/weave-agent/weave/sdk/model"
	"github.com/weave-agent/weave/sdk/providerhttp"
	"github.com/weave-agent/weave/sdk/providerretry"
	"github.com/weave-agent/weave/sdk/retry"
	openaicompat "github.com/weave-agent/weave/utils/openaicompat"
)

const (
	defaultCountBaseURL  = "https://api.z.ai/api/paas/v4"
	maxTokenizerBodySize = 64 * 1024
)

// ZaiConfig holds per-provider configuration for the Z.ai provider.
type ZaiConfig struct {
	Model            string `json:"model" default:"glm-5.1" env:"ZAI_MODEL" description:"Model name"`
	BaseURL          string `json:"base_url" default:"https://api.z.ai/api/coding/paas/v4" env:"ZAI_BASE_URL" description:"API base URL"`
	TokenizerBaseURL string `json:"tokenizer_base_url" default:"https://api.z.ai/api/paas/v4" env:"ZAI_TOKENIZER_BASE_URL" description:"Tokenizer API base URL"`
}

// AuthConfig holds authentication credentials for the Z.ai provider.
type AuthConfig struct {
	APIKey string `json:"api_key" env:"ZAI_API_KEY" description:"API key"`
}

type provider struct {
	client           *http.Client
	config           openaicompat.ProviderConfig
	tokenizerBaseURL string
}

type tokenizerResponse struct {
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

//nolint:gochecknoinits // Provider registration happens through package init hooks.
func init() {
	sdk.RegisterProvider[ZaiConfig, AuthConfig]("zai", func(cfg sdk.Config, zc ZaiConfig, a AuthConfig) (sdk.Provider, error) {
		if a.APIKey == "" {
			return nil, errors.New("zai: API key required (set ZAI_API_KEY)")
		}

		client, _, err := providerhttp.ForProvider(cfg, "zai")
		if err != nil {
			return nil, fmt.Errorf("zai: resolve HTTP config: %w", err)
		}

		retryConfig, _, err := providerretry.ForProvider(cfg, "zai")
		if err != nil {
			return nil, fmt.Errorf("zai: resolve retry config: %w", err)
		}

		return &provider{
			client:           client,
			tokenizerBaseURL: zc.TokenizerBaseURL,
			config: openaicompat.ProviderConfig{
				BaseURL:     zc.BaseURL,
				APIKey:      a.APIKey,
				Model:       zc.Model,
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
		}, nil
	})
}

func (p *provider) Stream(ctx context.Context, req sdk.ProviderRequest, opts ...model.StreamOption) (<-chan sdk.ProviderEvent, error) {
	ch, err := openaicompat.Stream(ctx, p.client, p.config, req, opts...)
	if err != nil {
		return nil, fmt.Errorf("zai: %w", err)
	}

	return ch, nil
}

func (p *provider) CountTokens(ctx context.Context, req sdk.ProviderRequest, opts ...model.StreamOption) (sdk.TokenCount, error) {
	so := model.NewStreamOptions(opts...)

	mdl := so.Model
	if mdl == "" {
		mdl = p.config.Model
	}

	messages, err := convertTokenizerMessages(req.Messages)
	if err != nil {
		return sdk.TokenCount{}, err
	}

	if req.SystemPrompt != "" {
		sysMsg := openaicompat.ChatMessage{Role: "system", Content: req.SystemPrompt}
		messages = append([]openaicompat.ChatMessage{sysMsg}, messages...)
	}

	if !tokenizerMessagesCountable(messages) {
		return sdk.TokenCount{}, errors.New("zai: tokenizer requires at least one user message")
	}

	body := map[string]any{
		"model":    mdl,
		"messages": messages,
	}

	if tools := openaicompat.ConvertTools(req.Tools); len(tools) > 0 {
		body["tools"] = tools
	}

	if p.config.ModifyRequest != nil {
		p.config.ModifyRequest(body, so)
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: marshal tokenizer request: %w", err)
	}

	rc := retry.DefaultConfig()
	if p.config.RetryConfig != nil {
		rc = *p.config.RetryConfig
	}

	respBody, err := p.doTokenizerRequestWithRetry(ctx, reqBody, rc)
	if err != nil {
		return sdk.TokenCount{}, err
	}

	var tokenizerResp tokenizerResponse
	if err := json.Unmarshal(respBody, &tokenizerResp); err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: parse tokenizer response: %w", err)
	}

	inputTokens := tokenizerResp.Usage.PromptTokens
	if inputTokens == 0 {
		return sdk.TokenCount{}, errors.New("zai: tokenizer response missing prompt_tokens")
	}

	return sdk.TokenCount{
		InputTokens: inputTokens,
		Source:      sdk.TokenCountSourceTokenizer,
		Confidence:  0.95,
	}, nil
}

func (p *provider) doTokenizerRequestWithRetry(ctx context.Context, reqBody []byte, rc retry.Config) ([]byte, error) {
	var respBody []byte

	err := retry.Do(ctx, rc, isTokenizerRetriableError, func() error {
		body, err := p.doTokenizerRequest(ctx, reqBody)
		if err != nil {
			return err
		}

		respBody = body

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("zai: tokenizer request after retry: %w", err)
	}

	return respBody, nil
}

func (p *provider) doTokenizerRequest(ctx context.Context, reqBody []byte) ([]byte, error) {
	tokenizerBaseURL := p.tokenizerBaseURL
	if tokenizerBaseURL == "" {
		tokenizerBaseURL = defaultCountBaseURL
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(tokenizerBaseURL, "/")+"/tokenizer", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("zai: create tokenizer request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)

	for k, v := range p.config.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &openaicompat.Error{
			Type:    openaicompat.ErrorTypeTransport,
			Message: err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenizerBodySize))
	if err != nil {
		return nil, fmt.Errorf("zai: read tokenizer response: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		return respBody, nil
	}

	var errResp openaicompat.ErrorResponse
	if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
		return nil, &openaicompat.Error{
			StatusCode: resp.StatusCode,
			Type:       tokenizerErrorType(resp.StatusCode),
			Message:    errResp.Error.Message,
			Body:       string(respBody),
		}
	}

	return nil, &openaicompat.Error{
		StatusCode: resp.StatusCode,
		Type:       tokenizerErrorType(resp.StatusCode),
		Message:    fmt.Sprintf("status %d: %s", resp.StatusCode, string(respBody)),
		Body:       string(respBody),
	}
}

func convertTokenizerMessages(msgs []sdk.Message) ([]openaicompat.ChatMessage, error) {
	var result []openaicompat.ChatMessage

	for _, msg := range msgs {
		switch msg.Role {
		case sdk.RoleUser:
			result = append(result, openaicompat.ChatMessage{
				Role:    "user",
				Content: fmt.Sprint(msg.Content),
			})
		case sdk.RoleAssistant:
			var content string
			if msg.Content != nil {
				content = fmt.Sprint(msg.Content)
			}

			if content != "" {
				result = append(result, openaicompat.ChatMessage{
					Role:    "assistant",
					Content: content,
				})
			}

			for _, tc := range msg.ToolCalls {
				content, err := tokenizerToolCallContent(tc)
				if err != nil {
					return nil, err
				}

				result = append(result, openaicompat.ChatMessage{
					Role:    "assistant",
					Content: content,
				})
			}
		case sdk.RoleToolResult:
			content, err := tokenizerToolResultContent(msg)
			if err != nil {
				return nil, err
			}

			result = append(result, openaicompat.ChatMessage{
				Role:    "user",
				Content: content,
			})
		}
	}

	return result, nil
}

func tokenizerMessagesCountable(messages []openaicompat.ChatMessage) bool {
	for _, msg := range messages {
		if msg.Role == "user" {
			return true
		}
	}

	return false
}

func tokenizerToolCallContent(tc sdk.ToolCall) (string, error) {
	payload := struct {
		ID        string `json:"id,omitempty"`
		Name      string `json:"name"`
		Arguments any    `json:"arguments,omitempty"`
	}{
		ID:        tc.ID,
		Name:      tc.Name,
		Arguments: tc.Arguments,
	}

	content, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("zai: marshal tokenizer tool call: %w", err)
	}

	return "tool_call " + string(content), nil
}

func tokenizerToolResultContent(msg sdk.Message) (string, error) {
	payload := struct {
		ToolCallID string `json:"tool_call_id,omitempty"`
		ToolName   string `json:"tool_name,omitempty"`
		IsError    bool   `json:"is_error,omitempty"`
		Content    any    `json:"content"`
	}{
		ToolCallID: msg.ToolCallID,
		ToolName:   msg.ToolName,
		IsError:    msg.IsError,
		Content:    msg.Content,
	}

	content, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("zai: marshal tokenizer tool result: %w", err)
	}

	return "tool_result " + string(content), nil
}

func tokenizerErrorType(code int) openaicompat.ErrorType {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return openaicompat.ErrorTypeAuth
	case code == http.StatusTooManyRequests:
		return openaicompat.ErrorTypeRateLimit
	case code >= http.StatusInternalServerError:
		return openaicompat.ErrorTypeServer
	default:
		return openaicompat.ErrorTypeClient
	}
}

func isTokenizerRetriableError(err error) bool {
	var apiErr *openaicompat.Error
	if errors.As(err, &apiErr) {
		return apiErr.IsRetriable()
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return false
}
