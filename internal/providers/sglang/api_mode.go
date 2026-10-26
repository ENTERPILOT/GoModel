package sglang

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// api_mode selects which SGLang endpoint serves a Responses API request.
// SGLang's /v1/responses handles plain and hosted-tool requests but fails on
// custom function tools (https://github.com/sgl-project/sglang/issues/13292),
// while its /v1/chat/completions handles function tools.
const (
	// apiModeAuto translates only requests carrying function tools.
	apiModeAuto = "auto"
	// apiModeNative always forwards to /v1/responses.
	apiModeNative = "native"
	// apiModeChatCompatible always translates through /v1/chat/completions.
	apiModeChatCompatible = "chat_compatible"
)

// parseAPIMode normalizes the configured api_mode; empty means auto.
func parseAPIMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "":
		return apiModeAuto, nil
	case apiModeAuto, apiModeNative, apiModeChatCompatible:
		return mode, nil
	default:
		return "", fmt.Errorf("api_mode must be %s, %s, or %s, got %q", apiModeAuto, apiModeNative, apiModeChatCompatible, value)
	}
}

// configError wraps an invalid configuration into the error every request
// method returns, so the misconfiguration surfaces instead of a silent default.
func configError(err error) error {
	return core.NewProviderError("sglang", http.StatusBadGateway, "invalid SGLang provider configuration: "+err.Error(), err)
}

// responsesViaChat reports whether a Responses request is translated to chat
// completions. The gateway expands previous_response_id from its response
// store before dispatch, because SGLang does not offer the native response
// lifecycle, so a chained request arrives here with its full history.
func (p *Provider) responsesViaChat(req *core.ResponsesRequest) bool {
	switch p.apiMode {
	case apiModeNative:
		return false
	case apiModeChatCompatible:
		return true
	default:
		return hasFunctionTool(req)
	}
}

// hasFunctionTool reports whether req declares a custom function tool. Hosted
// tools such as web_search have no chat equivalent, so a request with only
// those stays on /v1/responses.
func hasFunctionTool(req *core.ResponsesRequest) bool {
	if req == nil {
		return false
	}
	for _, tool := range req.Tools {
		if toolType, _ := tool["type"].(string); strings.TrimSpace(toolType) == "function" {
			return true
		}
	}
	return false
}
