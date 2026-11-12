package gateway

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// flatReasoningEffortField is the flat Chat Completions spelling of the
// reasoning effort, kept in ExtraFields when a client sends it.
const flatReasoningEffortField = "reasoning_effort"

// clientRequestKey keys the client's request as it was before preparation
// forced the primary attempt's overrides onto it, so a failover attempt is
// rebuilt from the client's request with its own overrides.
type clientRequestKey struct{}

func withClientRequest[Req any](ctx context.Context, req Req) context.Context {
	return context.WithValue(ctx, clientRequestKey{}, req)
}

// withRequestOverrides wraps one provider call so each attempt, primary or
// failover, carries the reasoning effort and request parameters the operator
// forced for the model it goes to, within the ceilings of the caller's user
// path. A primary attempt prepared with its
// overrides passes through untouched; a failover leg is rebuilt from the
// client's request so the primary's overrides never leak into it. The effort
// is written to the common reasoning.effort field, so every provider's
// existing reasoning translation applies to it unchanged.
func withRequestOverrides[Req any, T any](
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	selector func(Req) core.ModelSelector,
	apply func(Req, requestparams.Overrides) (Req, error),
	rebuild func(client, attempt Req) Req,
	call func(context.Context, Req, string) (T, error),
) func(context.Context, Req, string) (T, error) {
	resolver, ok := o.modelResolver.(ModelRequestOverridesResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return call
	}
	resolution := workflow.Resolution
	return func(ctx context.Context, req Req, providerName string) (T, error) {
		attempt := selector(req)
		if client, prepared := ctx.Value(clientRequestKey{}).(Req); prepared {
			if attempt.QualifiedModel() == resolution.ResolvedSelector.QualifiedModel() {
				return call(ctx, req, providerName)
			}
			req = rebuild(client, req)
		}
		overrides := resolver.ResolveRequestOverrides(ctx, resolution, attempt)
		ctx = withCeilingsContext(ctx, overrides.Ceilings)
		if !overrides.IsZero() {
			var err error
			if req, err = apply(req, overrides); err != nil {
				var zero T
				return zero, overridesError(err)
			}
		}
		return call(ctx, req, providerName)
	}
}

// withCeilingsContext attaches the ceilings to the provider call's context,
// so an adapter that fills in or raises a value of its own (a default token
// limit, room for a thinking budget, a default effort) keeps it within them.
func withCeilingsContext(ctx context.Context, ceilings requestparams.Ceilings) context.Context {
	return core.WithRequestCeilings(ctx, core.RequestCeilings{ReasoningEffort: ceilings.ReasoningEffort, OutputTokens: ceilings.OutputTokens})
}

// RequestOverrides returns what the operator forces on the primary attempt of
// workflow, or a zero value when nothing is configured. Paths that forward
// the client's body verbatim use it to fall back to the translated pipeline,
// which applies the overrides.
func (o *InferenceOrchestrator) RequestOverrides(ctx context.Context, workflow *core.Workflow) requestparams.Overrides {
	resolver, ok := o.modelResolver.(ModelRequestOverridesResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return requestparams.Overrides{}
	}
	return resolver.ResolveRequestOverrides(ctx, workflow.Resolution, workflow.Resolution.ResolvedSelector)
}

// rebuildChatAttempt returns the client's request re-addressed to attempt's
// model, keeping the stream settings the gateway added to the attempt after
// preparation (the usage flag on streamed requests).
func rebuildChatAttempt(client, attempt *core.ChatRequest) *core.ChatRequest {
	rebuilt := CloneChatRequestForSelector(client, chatRequestSelectorOf(attempt))
	rebuilt.Stream = attempt.Stream
	rebuilt.StreamOptions = attempt.StreamOptions
	return rebuilt
}

// rebuildResponsesAttempt is rebuildChatAttempt for Responses.
func rebuildResponsesAttempt(client, attempt *core.ResponsesRequest) *core.ResponsesRequest {
	rebuilt := CloneResponsesRequestForSelector(client, responsesRequestSelectorOf(attempt))
	rebuilt.Stream = attempt.Stream
	rebuilt.StreamOptions = attempt.StreamOptions
	return rebuilt
}

// overridesError reports a configured parameter the request type refused.
// Rows are validated when saved, so this is a gateway fault, not the
// client's.
func overridesError(err error) error {
	return core.NewInternalErrorWithStatus(http.StatusInternalServerError, "failed to apply configured request parameters", err)
}

// ChatRequestWithOverrides returns a copy of req with the configured
// parameters applied, then the reasoning effort forced, then the ceilings.
// The effort goes after the parameters so the typed override keeps winning
// over anything the parameter sets carry; the ceilings go last so they bound
// a forced or defaulted value as they bound the client's.
func ChatRequestWithOverrides(req *core.ChatRequest, overrides requestparams.Overrides) (*core.ChatRequest, error) {
	applied, err := requestparams.ApplyChat(req, overrides.Defaults, overrides.Params)
	if err != nil {
		return nil, err
	}
	if overrides.ReasoningEffort != "" {
		applied = ChatRequestWithReasoningEffort(applied, overrides.ReasoningEffort)
	}
	return ChatRequestWithCeilings(applied, overrides.Ceilings)
}

// ResponsesRequestWithOverrides is ChatRequestWithOverrides for Responses.
func ResponsesRequestWithOverrides(req *core.ResponsesRequest, overrides requestparams.Overrides) (*core.ResponsesRequest, error) {
	applied, err := requestparams.ApplyResponses(req, overrides.Defaults, overrides.Params)
	if err != nil {
		return nil, err
	}
	if overrides.ReasoningEffort != "" {
		applied = ResponsesRequestWithReasoningEffort(applied, overrides.ReasoningEffort)
	}
	return ResponsesRequestWithCeilings(applied, overrides.Ceilings)
}

func chatRequestSelectorOf(req *core.ChatRequest) core.ModelSelector {
	return core.ModelSelector{Provider: req.Provider, Model: req.Model}
}

func responsesRequestSelectorOf(req *core.ResponsesRequest) core.ModelSelector {
	return core.ModelSelector{Provider: req.Provider, Model: req.Model}
}

// ChatRequestWithReasoningEffort returns a shallow copy of req whose reasoning
// effort is effort. The client's own effort settings (a flat reasoning_effort,
// a Gemini thinking budget or level) are dropped so no provider reads them
// instead.
func ChatRequestWithReasoningEffort(req *core.ChatRequest, effort string) *core.ChatRequest {
	return chatRequestWithEffort(req, effort, withoutClientEffort)
}

// ResponsesRequestWithReasoningEffort returns a shallow copy of req whose
// reasoning effort is effort.
func ResponsesRequestWithReasoningEffort(req *core.ResponsesRequest, effort string) *core.ResponsesRequest {
	return responsesRequestWithEffort(req, effort, withoutClientEffort)
}

// chatRequestWithEffort returns a shallow copy of req whose reasoning effort
// is effort and whose extra fields went through strip.
func chatRequestWithEffort(req *core.ChatRequest, effort string, strip func(core.UnknownJSONFields) core.UnknownJSONFields) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Reasoning = &core.Reasoning{Effort: effort}
	cloned.ExtraFields = strip(req.ExtraFields)
	return &cloned
}

// responsesRequestWithEffort is chatRequestWithEffort for Responses.
func responsesRequestWithEffort(req *core.ResponsesRequest, effort string, strip func(core.UnknownJSONFields) core.UnknownJSONFields) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	reasoning := core.Reasoning{Effort: effort}
	if req.Reasoning != nil {
		// An override replaces the effort, not a summary the client
		// explicitly requested.
		reasoning.Summary = req.Reasoning.Summary
	}
	cloned.Reasoning = &reasoning
	cloned.ExtraFields = strip(req.ExtraFields)
	return &cloned
}
