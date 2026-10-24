package openai

import (
	"context"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResponsesUpstream is the Responses surface ChatViaResponses serves a chat
// request on.
type ResponsesUpstream interface {
	Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error)
	StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error)
}

// ChatViaResponses serves a chat request on a model its upstream exposes only
// through the Responses API, rendering the result as a chat completion. It
// uses the same translation the OpenAI provider routes with, but has no Chat
// Completions endpoint to fall back on: a request the translation cannot carry
// exactly is rejected instead of being sent with fields dropped.
func ChatViaResponses(ctx context.Context, upstream ResponsesUpstream, req *core.ChatRequest, providerName string) (*core.ChatResponse, error) {
	req, stops, err := takeStopSequences(req)
	if err != nil {
		return nil, err
	}
	responsesReq, err := responsesOnlyRequest(req)
	if err != nil {
		return nil, err
	}
	routed, err := upstream.Responses(ctx, responsesReq)
	if err != nil {
		return nil, err
	}
	resp, err := chatResponseFromResponses(routed, providerName)
	if err != nil {
		return nil, err
	}
	applyStopSequences(resp, stops)
	return resp, nil
}

// StreamChatViaResponses is ChatViaResponses for streaming requests: the
// Responses stream is converted into chat completion chunks.
func StreamChatViaResponses(ctx context.Context, upstream ResponsesUpstream, req *core.ChatRequest, providerName string) (io.ReadCloser, error) {
	req, stops, err := takeStopSequences(req)
	if err != nil {
		return nil, err
	}
	responsesReq, err := responsesOnlyRequest(req)
	if err != nil {
		return nil, err
	}
	routed, err := upstream.StreamResponses(ctx, responsesReq)
	if err != nil {
		return nil, err
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	stream := newResponsesChatStream(routed, providerName, req.Model, includeUsage)
	if len(stops) > 0 {
		stream = newStopSequenceStream(stream, stops)
	}
	return stream, nil
}

// responsesOnlyRequest translates a chat request for a Responses-only model.
func responsesOnlyRequest(req *core.ChatRequest) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	out, err := chatToResponsesRequest(req)
	if err != nil {
		return nil, core.NewInvalidRequestError(
			"model "+req.Model+" is served only through the Responses API and this chat request cannot be translated to it exactly; send it to /v1/responses instead",
			err)
	}
	return out, nil
}
