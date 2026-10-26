package gateway

import (
	"context"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// flatReasoningEffortField is the flat Chat Completions spelling of the
// reasoning effort, kept in ExtraFields when a client sends it.
const flatReasoningEffortField = "reasoning_effort"

// googleThinkingEffortFields are the extra_body.google.thinking_config members
// that set Gemini's effort. A forced effort drops them so the client's budget
// or level cannot win over it; other members such as include_thoughts stay.
var googleThinkingEffortFields = []string{"thinking_budget", "thinkingBudget", "thinking_level", "thinkingLevel"}

// clientRequestKey keys the client's request as it was before preparation
// forced the primary attempt's effort onto it, so a failover attempt without
// its own override gets the client's reasoning back.
type clientRequestKey struct{}

func withClientRequest[Req any](ctx context.Context, req Req) context.Context {
	return context.WithValue(ctx, clientRequestKey{}, req)
}

// withReasoningEffort wraps one provider call so each attempt, primary or
// failover, carries the reasoning effort the operator forced for the model it
// goes to. The override is written to the common reasoning.effort field, so
// every provider's existing reasoning translation applies to it unchanged.
func withReasoningEffort[Req any, T any](
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	selector func(Req) core.ModelSelector,
	apply func(Req, string) Req,
	restore func(Req, Req) Req,
	call func(context.Context, Req, string) (T, error),
) func(context.Context, Req, string) (T, error) {
	resolver, ok := o.modelResolver.(ModelReasoningEffortResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return call
	}
	resolution := workflow.Resolution
	return func(ctx context.Context, req Req, providerName string) (T, error) {
		if effort := resolver.ResolveReasoningEffort(ctx, resolution, selector(req)); effort != "" {
			req = apply(req, effort)
		} else if client, ok := ctx.Value(clientRequestKey{}).(Req); ok {
			req = restore(req, client)
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
	return resolver.ResolveReasoningEffort(ctx, workflow.Resolution, workflow.Resolution.ResolvedSelector)
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
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Reasoning = &core.Reasoning{Effort: effort}
	cloned.ExtraFields = withoutClientEffort(req.ExtraFields)
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
	cloned.ExtraFields = withoutClientEffort(req.ExtraFields)
	return &cloned
}

// chatRequestWithClientReasoning returns a shallow copy of req carrying the
// client's reasoning settings from client.
func chatRequestWithClientReasoning(req, client *core.ChatRequest) *core.ChatRequest {
	cloned := *req
	cloned.Reasoning = client.Reasoning
	cloned.ExtraFields = client.ExtraFields
	return &cloned
}

func responsesRequestWithClientReasoning(req, client *core.ResponsesRequest) *core.ResponsesRequest {
	cloned := *req
	cloned.Reasoning = client.Reasoning
	cloned.ExtraFields = client.ExtraFields
	return &cloned
}

func withoutClientEffort(fields core.UnknownJSONFields) core.UnknownJSONFields {
	fields = fields.Without(flatReasoningEffortField)
	extraBody, changed := withoutGoogleThinkingEffort(fields.Lookup("extra_body"))
	if !changed {
		return fields
	}
	rest := fields.Without("extra_body")
	if extraBody == nil {
		return rest
	}
	merged, err := core.MergeUnknownJSONFields(rest, map[string]json.RawMessage{"extra_body": extraBody})
	if err != nil {
		return fields
	}
	return merged
}

// withoutGoogleThinkingEffort removes the effort members from
// extra_body.google.thinking_config, dropping objects left empty. It returns
// nil when nothing of extra_body remains, and false when nothing changed.
func withoutGoogleThinkingEffort(extraBody json.RawMessage) (json.RawMessage, bool) {
	if len(extraBody) == 0 {
		return extraBody, false
	}
	var body, google, thinking map[string]json.RawMessage
	if json.Unmarshal(extraBody, &body) != nil || json.Unmarshal(body["google"], &google) != nil || json.Unmarshal(google["thinking_config"], &thinking) != nil {
		return extraBody, false
	}
	changed := false
	for _, field := range googleThinkingEffortFields {
		if _, ok := thinking[field]; ok {
			delete(thinking, field)
			changed = true
		}
	}
	if !changed {
		return extraBody, false
	}
	if err := setOrDelete(google, "thinking_config", thinking); err != nil {
		return extraBody, false
	}
	if err := setOrDelete(body, "google", google); err != nil {
		return extraBody, false
	}
	if len(body) == 0 {
		return nil, true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return extraBody, false
	}
	return raw, true
}

// setOrDelete stores value under key in parent, or removes key when value is
// empty.
func setOrDelete(parent map[string]json.RawMessage, key string, value map[string]json.RawMessage) error {
	if len(value) == 0 {
		delete(parent, key)
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	parent[key] = raw
	return nil
}
