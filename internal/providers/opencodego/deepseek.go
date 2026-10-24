package opencodego

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/deepseek"
)

// chatRequestAdapter composes the /chat/completions request hook: OpenCode
// Zen's reasoning-effort mapping for every model, then DeepSeek's request
// requirements for DeepSeek models. The hook runs after virtual-model routing
// has resolved this provider and model, so clients that cannot know DeepSeek
// serves the request still get the adaptation.
func chatRequestAdapter(defaultEffort string, jsonSchemaMode deepseek.JSONSchemaMode) func(*core.ChatRequest) (*core.ChatRequest, error) {
	adaptReasoning := adaptChatRequest(defaultEffort)
	return func(req *core.ChatRequest) (*core.ChatRequest, error) {
		adapted, err := adaptReasoning(req)
		if err != nil || adapted == nil || !deepseek.IsModel(adapted.Model) {
			return adapted, err
		}
		return deepseek.AdaptCompatibility(adapted, "opencode_go", jsonSchemaMode)
	}
}
