package zai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weave-agent/weave/sdk"
	"github.com/weave-agent/weave/sdk/model"
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

func init() {
	sdk.RegisterProvider[ZaiConfig, AuthConfig]("zai", func(cfg sdk.Config, zc ZaiConfig, a AuthConfig) (sdk.Provider, error) {
		if a.APIKey == "" {
			return nil, errors.New("zai: API key required (set ZAI_API_KEY)")
		}

		return &provider{
			client: &http.Client{},
			config: openaicompat.ProviderConfig{
				BaseURL: zc.BaseURL,
				APIKey:  a.APIKey,
				Model:   zc.Model,
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
