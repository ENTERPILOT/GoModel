package minimax

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// MiniMax's reasoning models keep the answer content clean only when the
// request asks them to split the chain of thought out of it: with
// reasoning_split set, the reasoning arrives natively in reasoning_content
// (plus a redundant reasoning_details array) and content carries just the
// answer, so no response normalization is needed.
const (
	reasoningSplitKey   = "reasoning_split"
	reasoningContentKey = "reasoning_content"
	reasoningDetailsKey = "reasoning_details"
)

// isReasoningModel reports whether the model is one of MiniMax's reasoning
// models, matched case-insensitively against an exact allowlist. The other
// MiniMax models reject reasoning_split as an unknown member, so the
// adaptation must not fire for them.
func isReasoningModel(model string) bool {
	switch strings.ToLower(model) {
	case
		"minimax-m2",
		"minimax-m2.5",
		"minimax-m2.5-highspeed",
		"minimax-m2.7",
		"minimax-m2.7-highspeed",
		"minimax-m3",
		"minimax-m3.1-flash":
		return true
	default:
		return false
	}
}

// adaptFlashEffort translates the gateway's common reasoning shape, including
// Responses requests, into Flash's native reasoning_effort parameter.
func adaptFlashEffort(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || !strings.EqualFold(req.Model, "MiniMax-M3.1-Flash") || req.Reasoning == nil {
		return req, nil
	}
	if req.ExtraFields.Lookup("reasoning_effort") != nil || req.Reasoning.Effort == "" {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, req.Reasoning.Effort)
}

// adaptChatRequest merges MiniMax's reasoning_split flag into the request's
// unknown members on the reasoning models. A caller that set the flag keeps
// its own value; any other request is returned unchanged.
func adaptChatRequest(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || !isReasoningModel(req.Model) || req.ExtraFields.Lookup(reasoningSplitKey) != nil {
		return req
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		reasoningSplitKey: json.RawMessage(`true`),
	})
	if err != nil {
		return req
	}
	adapted := *req
	adapted.ExtraFields = extra
	return &adapted
}
