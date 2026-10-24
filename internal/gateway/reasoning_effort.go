package gateway

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// flatReasoningEffortField is the flat Chat Completions spelling of the
// reasoning effort, kept in ExtraFields when a client sends it.
const flatReasoningEffortField = "reasoning_effort"

// withReasoningEffort wraps one provider call so each attempt, primary or
// failover, carries the reasoning effort the operator forced for the model it
// goes to. The override is written to the common reasoning.effort field, so
// every provider's existing reasoning translation applies to it unchanged.
func withReasoningEffort[Req any, T any](
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	selector func(Req) core.ModelSelector,
	apply func(Req, string) Req,
	call func(context.Context, Req, string) (T, error),
) func(context.Context, Req, string) (T, error) {
	resolver, ok := o.modelResolver.(ModelReasoningEffortResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return call
	}
	requested := workflow.Resolution.Requested
	return func(ctx context.Context, req Req, providerName string) (T, error) {
		if effort := resolver.ResolveReasoningEffort(ctx, requested, selector(req)); effort != "" {
			req = apply(req, effort)
		}
		return call(ctx, req, providerName)
	}
}

// ReasoningEffortOverride returns the reasoning effort forced on the primary
// attempt of workflow, or "" when none is configured. Paths that forward the
// client's body verbatim use it to fall back to the translated pipeline,
// which applies the override.
func (o *InferenceOrchestrator) ReasoningEffortOverride(ctx context.Context, workflow *core.Workflow) string {
	resolver, ok := o.modelResolver.(ModelReasoningEffortResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return ""
	}
	return resolver.ResolveReasoningEffort(ctx, workflow.Resolution.Requested, workflow.Resolution.ResolvedSelector)
}

func chatRequestSelectorOf(req *core.ChatRequest) core.ModelSelector {
	return core.ModelSelector{Provider: req.Provider, Model: req.Model}
}

func responsesRequestSelectorOf(req *core.ResponsesRequest) core.ModelSelector {
	return core.ModelSelector{Provider: req.Provider, Model: req.Model}
}

// ChatRequestWithReasoningEffort returns a shallow copy of req whose reasoning
// effort is effort. A flat reasoning_effort the client sent is dropped so no
// provider reads the client's value instead.
func ChatRequestWithReasoningEffort(req *core.ChatRequest, effort string) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Reasoning = &core.Reasoning{Effort: effort}
	cloned.ExtraFields = req.ExtraFields.Without(flatReasoningEffortField)
	return &cloned
}

// ResponsesRequestWithReasoningEffort returns a shallow copy of req whose
// reasoning effort is effort.
func ResponsesRequestWithReasoningEffort(req *core.ResponsesRequest, effort string) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Reasoning = &core.Reasoning{Effort: effort}
	cloned.ExtraFields = req.ExtraFields.Without(flatReasoningEffortField)
	return &cloned
}
