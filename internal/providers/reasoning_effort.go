package providers

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// AdaptReasoningEffortRequest rewrites GoModel's common nested reasoning shape
// into the flat "reasoning_effort" string extension used by several
// OpenAI-compatible providers (Gemini, DeepSeek). It shallow-copies the typed
// request and merges the effort into ExtraFields, so the body is marshaled
// only once, by the HTTP client. Other reasoning fields (e.g. budget_tokens)
// are dropped: these providers accept the flat string only.
func AdaptReasoningEffortRequest(req *core.ChatRequest, effort string) (*core.ChatRequest, error) {
	adapted := *req
	adapted.Reasoning = nil
	encodedEffort, err := json.Marshal(effort)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"reasoning_effort": encodedEffort,
	})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
	}
	adapted.ExtraFields = extra
	return &adapted, nil
}
