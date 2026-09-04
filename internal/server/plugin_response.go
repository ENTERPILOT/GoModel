package server

import (
	"errors"
	"log/slog"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/pluginapi"
)

// responsePhase runs the response chain over one non-streaming completion.
// It returns the (possibly replaced or edited) response, or a gateway error
// for block and fail-closed failures.
type responsePhase[Req any, Resp any] struct {
	fromRequest  func(Req) (*pluginapi.Prompt, error)
	fromResponse func(Resp) (*pluginapi.Completion, error)
	apply        func(Resp, *pluginapi.Completion) (Resp, error)
	synthesize   func(*pluginapi.Completion, string) Resp
	model        func(Resp) string
}

var chatResponsePhase = responsePhase[*core.ChatRequest, *core.ChatResponse]{
	fromRequest:  exchange.FromChatRequest,
	fromResponse: exchange.FromChatResponse,
	apply:        exchange.ApplyToChatResponse,
	synthesize:   exchange.CompletionToChatResponse,
	model:        func(resp *core.ChatResponse) string { return resp.Model },
}

var responsesResponsePhase = responsePhase[*core.ResponsesRequest, *core.ResponsesResponse]{
	fromRequest:  exchange.FromResponsesRequest,
	fromResponse: exchange.FromResponsesResponse,
	apply:        exchange.ApplyToResponsesResponse,
	synthesize:   exchange.CompletionToResponsesResponse,
	model:        func(resp *core.ResponsesResponse) string { return resp.Model },
}

func (p responsePhase[Req, Resp]) run(s *translatedInferenceService, c *echo.Context, workflow *core.Workflow, req Req, resp Resp) (Resp, error) {
	ctx := c.Request().Context()
	chains := s.pluginChainsFor(ctx)
	if chains == nil || chains.Response.Empty() {
		return resp, nil
	}
	state := plugins.RequestStateFromContext(ctx)
	if state == nil {
		state = plugins.NewRequestState()
	}
	x := state.NewExchange(ctx, pluginMeta(ctx, workflow))
	if prompt, err := p.fromRequest(req); err == nil {
		x.Prompt = prompt
	}
	completion, err := p.fromResponse(resp)
	if err != nil {
		var zero Resp
		return zero, core.NewProviderError("", 502, "response could not be mapped for plugins", err)
	}
	x.Response = completion

	outcome, runErr := chains.Response.RunResponse(ctx, x)
	state.Finish(x)
	requestID := requestIDFromContextOrHeader(c.Request())
	logResponseDecisions(requestID, pluginapi.KindResponse, outcome, state)
	if runErr != nil {
		var zero Resp
		if pluginErr, ok := errors.AsType[*plugins.PluginError](runErr); ok {
			slog.Warn("response plugin failed closed", "request_id", requestID, "instance", pluginErr.Instance, "error", pluginErr.Err)
			return zero, plugins.FailureError(runErr)
		}
		return zero, runErr
	}
	switch outcome.Decision.Action {
	case pluginapi.ActionBlock:
		var zero Resp
		return zero, plugins.BlockError(outcome.Decision, plugins.DefaultBlockStatus(pluginapi.KindResponse))
	case pluginapi.ActionRespond:
		return p.synthesize(outcome.Decision.Response, p.model(resp)), nil
	case pluginapi.ActionWarn:
		state.AddResponseHeader(plugins.GuardrailHeader, plugins.WarnHeaderValue(outcome.Decision))
	}
	if !completion.Changes().Dirty {
		return resp, nil
	}
	applied, err := p.apply(resp, completion)
	if err != nil {
		var zero Resp
		return zero, core.NewProviderError("", 502, "plugin produced an invalid response: "+err.Error(), err)
	}
	return applied, nil
}
