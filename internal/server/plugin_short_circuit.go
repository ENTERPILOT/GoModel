package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// shortCircuitOf returns the prompt-phase short-circuit behind err, if any.
func shortCircuitOf(err error) *plugins.ShortCircuit {
	if short, ok := errors.AsType[*plugins.ShortCircuit](err); ok {
		return short
	}
	return nil
}

// syntheticMeta describes the resolved route for a response the gateway
// produced itself.
func syntheticMeta(workflow *core.Workflow, fallbackModel string) gateway.ExecutionMeta {
	meta := gateway.ExecutionMeta{Model: resolvedModelFromWorkflow(workflow, fallbackModel)}
	if workflow != nil {
		meta.ProviderType = workflow.ProviderType
		meta.ProviderName = gateway.ProviderNameFromWorkflow(workflow)
	}
	return meta
}

// writeChatShortCircuit renders a plugin's completion as the chat response,
// as JSON or as a synthesized single-turn stream. The Anthropic dialect is
// handled by outerWrap (stream) and toJSON (non-stream) the same way as a
// provider response.
func (s *translatedInferenceService) writeChatShortCircuit(
	c *echo.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
	short *plugins.ShortCircuit,
	toJSON func(*core.ChatResponse) any,
	outerWrap func(io.ReadCloser) io.ReadCloser,
) error {
	model := ""
	if req != nil {
		model = req.Model
	}
	resp := exchange.CompletionToChatResponse(short.Completion, resolvedModelFromWorkflow(workflow, model))
	applyPluginResponseHeaders(c)
	if req == nil || !req.Stream {
		return c.JSON(http.StatusOK, toJSON(resp))
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	stream := io.NopCloser(bytes.NewReader(streaming.SynthesizeChatStream(resp, includeUsage)))
	return s.handleStreamingReadCloser(c, workflow, syntheticMeta(workflow, model), stream, outerWrap)
}

// writeResponsesShortCircuit renders a plugin's completion as a Responses
// API response or stream.
func (s *translatedInferenceService) writeResponsesShortCircuit(c *echo.Context, workflow *core.Workflow, req *core.ResponsesRequest, short *plugins.ShortCircuit) error {
	model := ""
	if req != nil {
		model = req.Model
	}
	resp := exchange.CompletionToResponsesResponse(short.Completion, resolvedModelFromWorkflow(workflow, model))
	applyPluginResponseHeaders(c)
	if req == nil || !req.Stream {
		return c.JSON(http.StatusOK, resp)
	}
	stream := io.NopCloser(bytes.NewReader(streaming.SynthesizeResponsesStream(resp)))
	return s.handleStreamingReadCloser(c, workflow, syntheticMeta(workflow, model), stream, nil)
}

// messagesOuterWrap converts a canonical chat stream into the Anthropic
// Messages SSE dialect.
func messagesOuterWrap(req *core.ChatRequest, model string) func(io.ReadCloser) io.ReadCloser {
	return func(stream io.ReadCloser) io.ReadCloser {
		return anthropicapi.NewStreamConverter(stream, model, anthropicapi.EstimateChatInputTokens(req))
	}
}

func messagesJSON(resp *core.ChatResponse) any { return anthropicapi.FromChatResponse(resp) }

func chatJSON(resp *core.ChatResponse) any { return resp }

// prepareContext returns the context to render a prompt-phase outcome with:
// the prepared one when the workflow was resolved, else the request's.
func prepareContext(c *echo.Context, ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return c.Request().Context()
}
