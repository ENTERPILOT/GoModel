package chatgpt

import (
	"github.com/enterpilot/gomodel/internal/core"
)

// upstreamRequest is the request body the ChatGPT Codex backend accepts.
//
// The backend validates against a strict allowlist and rejects any field
// outside it — including ones the public Responses API supports (temperature,
// top_p, max_output_tokens, previous_response_id, truncation, metadata, user,
// service_tier). Building the body from this struct rather than filtering the
// incoming request keeps that contract explicit and stops new gateway fields
// from silently breaking every request.
type upstreamRequest struct {
	Model             string           `json:"model"`
	Input             any              `json:"input"`
	Instructions      string           `json:"instructions,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Reasoning         *core.Reasoning  `json:"reasoning,omitempty"`
	Text              any              `json:"text,omitempty"`
	Include           []string         `json:"include,omitempty"`
	// Stream and Store are pinned: the backend rejects `stream: false` and
	// `store: true` outright.
	Stream bool `json:"stream"`
	Store  bool `json:"store"`
}

// newUpstreamRequest adapts a gateway Responses request to the Codex backend
// dialect, dropping unsupported parameters rather than failing the request.
func newUpstreamRequest(req *core.ResponsesRequest) (*upstreamRequest, error) {
	input, err := normalizeInput(req.Input)
	if err != nil {
		return nil, err
	}
	return &upstreamRequest{
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
		Store:             false,
	}, nil
}

// normalizeInput wraps a bare string prompt in the message list the backend
// requires; array inputs pass through untouched.
func normalizeInput(input any) (any, error) {
	switch v := input.(type) {
	case nil:
		return nil, core.NewInvalidRequestError("responses input is required", nil)
	case string:
		return []core.ResponsesInputElement{{
			Type:    "message",
			Role:    "user",
			Content: []core.ContentPart{{Type: "input_text", Text: v}},
		}}, nil
	default:
		return v, nil
	}
}
