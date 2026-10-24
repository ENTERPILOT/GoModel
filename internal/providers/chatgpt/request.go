package chatgpt

import (
	"context"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// passthroughFields are backend-accepted fields the gateway keeps in
// ResponsesRequest.ExtraFields. Each is one the Codex CLI itself sends.
var passthroughFields = []string{
	// prompt_cache_key partitions the backend prefix cache per conversation;
	// without it every turn lands on a cold cache.
	promptCacheKeyField,
}

const (
	promptCacheKeyField = "prompt_cache_key"
	// maxPromptCacheKeyLength is the Responses API limit on prompt_cache_key.
	// A longer gateway session id is not substituted for a missing key.
	maxPromptCacheKeyLength = 64
)

// newUpstreamRequest adapts a gateway Responses request to the Codex backend
// dialect, dropping unsupported parameters rather than failing the request.
//
// The backend validates against a strict allowlist and rejects any field
// outside it — including ones the public Responses API supports (temperature,
// top_p, max_output_tokens, previous_response_id, truncation, metadata, user,
// service_tier). Building the body from the allowed fields rather than
// filtering the incoming request keeps that contract explicit and stops new
// gateway fields from silently breaking every request. The allowlist spans
// both halves of a gateway request: typed fields are copied below, untyped
// ones from ExtraFields when named in passthroughFields.
//
// Stream and Store are pinned: the backend rejects `stream: false` and
// `store: true` outright.
func newUpstreamRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	input, err := normalizeInput(req.Input)
	if err != nil {
		return nil, err
	}
	store := false
	return &core.ResponsesRequest{
		Model:             req.Model,
		Input:             input,
		Instructions:      req.Instructions,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Reasoning:         req.Reasoning,
		Text:              req.Text,
		Include:           req.Include,
		Stream:            true,
		Store:             &store,
		ExtraFields:       passthroughExtras(ctx, req.ExtraFields),
	}, nil
}

// passthroughExtras keeps the allowlisted untyped fields; an explicit null
// counts as absent. When the client sent no prompt_cache_key, the session
// GoModel detected stands in for it — the Codex CLI uses its session id as the
// key too — so clients that send none still get cache affinity.
func passthroughExtras(ctx context.Context, fields core.UnknownJSONFields) core.UnknownJSONFields {
	kept := make(map[string]json.RawMessage, len(passthroughFields))
	for _, name := range passthroughFields {
		if raw := fields.Lookup(name); !core.IsJSONNull(raw) {
			kept[name] = raw
		}
	}
	if _, ok := kept[promptCacheKeyField]; !ok {
		if id := core.SessionIDFromContext(ctx); id != "" && len(id) <= maxPromptCacheKeyLength {
			kept[promptCacheKeyField], _ = json.Marshal(id)
		}
	}
	return core.UnknownJSONFieldsFromMap(kept)
}

// normalizeInput wraps a bare string prompt in the message list the backend
// requires; array inputs pass through untouched.
//
// The content part is a literal rather than a core.ContentPart: that type
// marshals to the Chat Completions shape, rewriting "input_text" to "text",
// which is not how the Responses API spells input content.
func normalizeInput(input any) (any, error) {
	switch v := input.(type) {
	case nil:
		return nil, core.NewInvalidRequestError("responses input is required", nil)
	case string:
		return []core.ResponsesInputElement{{
			Type:    "message",
			Role:    "user",
			Content: []map[string]string{{"type": "input_text", "text": v}},
		}}, nil
	default:
		return v, nil
	}
}
