package zai

import (
	"github.com/weave-agent/weave/sdk/model"
)

const providerName = "zai"

func init() {
	RegisterModels()
}

// RegisterModels registers ZAI model definitions in the global model registry.
func RegisterModels() {
	model.RegisterModel(model.ModelDef{
		ID: "glm-5.1", Provider: providerName,
		DisplayName: "GLM-5.1", Reasoning: true,
		ContextWindow: 200000, MaxTokens: 131072, Default: true,
	})
	model.RegisterModel(model.ModelDef{
		ID: "glm-5", Provider: providerName,
		DisplayName: "GLM-5", Reasoning: true,
		ContextWindow: 204800, MaxTokens: 131072,
	})
	model.RegisterModel(model.ModelDef{
		ID: "glm-4.7", Provider: providerName,
		DisplayName: "GLM-4.7", Reasoning: true,
		ContextWindow: 204800, MaxTokens: 131072,
	})
	model.RegisterModel(model.ModelDef{
		ID: "glm-4.7-flash", Provider: providerName,
		DisplayName: "GLM-4.7 Flash", Reasoning: true,
		ContextWindow: 200000, MaxTokens: 131072,
	})
	model.RegisterModel(model.ModelDef{
		ID: "glm-4.7-flashx", Provider: providerName,
		DisplayName: "GLM-4.7 FlashX", Reasoning: true,
		ContextWindow: 200000, MaxTokens: 131072,
	})
	model.RegisterModel(model.ModelDef{
		ID: "glm-4.5-air", Provider: providerName,
		DisplayName: "GLM-4.5 Air", Reasoning: true,
		ContextWindow: 131072, MaxTokens: 98304,
	})
}
