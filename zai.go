package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/weave-agent/weave/sdk"
	"github.com/weave-agent/weave/sdk/model"
	"github.com/weave-agent/weave/sdk/providerhttp"
	"github.com/weave-agent/weave/sdk/providerretry"
	openaicompat "github.com/weave-agent/weave/utils/openaicompat"
)

// ZaiConfig holds per-provider configuration for the Z.ai provider.
type ZaiConfig struct {
	Model   string `json:"model" default:"glm-5.1" env:"ZAI_MODEL" description:"Model name"`
	BaseURL string `json:"base_url" default:"https://api.z.ai/api/coding/paas/v4" env:"ZAI_BASE_URL" description:"API base URL"`
}

// AuthConfig holds authentication credentials for the Z.ai provider.
type AuthConfig struct {
	APIKey string `json:"api_key" env:"ZAI_API_KEY" description:"API key"`
}

type provider struct {
	client *http.Client
	config openaicompat.ProviderConfig
}

type tokenizerRequest struct {
	Model    string                     `json:"model"`
	Messages []openaicompat.ChatMessage `json:"messages"`
	Tools    []openaicompat.Tool        `json:"tools,omitempty"`
}

type tokenizerResponse struct {
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

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
			client: client,
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

	tokenizerReq := tokenizerRequest{
		Model:    mdl,
		Messages: openaicompat.ConvertMessages(req.Messages),
		Tools:    openaicompat.ConvertTools(req.Tools),
	}

	if req.SystemPrompt != "" {
		sysMsg := openaicompat.ChatMessage{Role: "system", Content: req.SystemPrompt}
		tokenizerReq.Messages = append([]openaicompat.ChatMessage{sysMsg}, tokenizerReq.Messages...)
	}

	reqBody, err := json.Marshal(tokenizerReq)
	if err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: marshal tokenizer request: %w", err)
	}

	if p.config.ModifyRequest != nil {
		var bodyMap map[string]any
		if unmarshalErr := json.Unmarshal(reqBody, &bodyMap); unmarshalErr != nil {
			return sdk.TokenCount{}, fmt.Errorf("zai: unmarshal tokenizer request: %w", unmarshalErr)
		}

		p.config.ModifyRequest(bodyMap, so)

		reqBody, err = json.Marshal(bodyMap)
		if err != nil {
			return sdk.TokenCount{}, fmt.Errorf("zai: marshal modified tokenizer request: %w", err)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/tokenizer", bytes.NewReader(reqBody))
	if err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: create tokenizer request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)

	for k, v := range p.config.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: tokenizer request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: read tokenizer response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp openaicompat.ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return sdk.TokenCount{}, fmt.Errorf("zai: tokenizer error: %s", errResp.Error.Message)
		}

		return sdk.TokenCount{}, fmt.Errorf("zai: tokenizer error: status %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenizerResp tokenizerResponse
	if err := json.Unmarshal(respBody, &tokenizerResp); err != nil {
		return sdk.TokenCount{}, fmt.Errorf("zai: parse tokenizer response: %w", err)
	}

	inputTokens := tokenizerResp.Usage.PromptTokens
	if inputTokens == 0 {
		inputTokens = tokenizerResp.Usage.TotalTokens
	}

	return sdk.TokenCount{
		InputTokens: inputTokens,
		Source:      sdk.TokenCountSourceTokenizer,
		Confidence:  0.95,
	}, nil
}
