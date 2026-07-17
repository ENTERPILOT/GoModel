package bedrockmantle

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func requestRouter(mode string) func(*llmclient.Request) {
	return func(req *llmclient.Request) {
		if req.Endpoint == "/models" {
			req.Endpoint = "/v1/models"
			return
		}
		prefix := "/v1"
		if mode == modeOpenAI || (mode == modeAuto && usesOpenAIPath(requestModel(req.Body))) {
			prefix = "/openai/v1"
		}
		req.Endpoint = prefix + req.Endpoint
	}
}

func requestModel(body any) string {
	switch req := body.(type) {
	case *core.ChatRequest:
		if req != nil {
			return req.Model
		}
	case *core.ResponsesRequest:
		if req != nil {
			return req.Model
		}
	}
	return ""
}

// AWS exposes a second OpenAI-compatible route for select model families.
// These models reject the generic /v1 route even though their payloads use
// the same OpenAI schema. Keep the list narrow; api_mode lets operators force
// either route when AWS adds a model before GoModel is updated.
func usesOpenAIPath(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "openai.gpt-5.") ||
		strings.HasPrefix(model, "google.gemma-4-") ||
		strings.HasPrefix(model, "xai.grok-4.3")
}
