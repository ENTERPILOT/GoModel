package gateway

import (
	"context"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// flatReasoningEffortField is the flat Chat Completions spelling of the
// reasoning effort, kept in ExtraFields when a client sends it.
const flatReasoningEffortField = "reasoning_effort"

// googleThinkingEffortFields are the extra_body.google.thinking_config members
// that set Gemini's effort. A forced effort drops them so the client's budget
// or level cannot win over it; other members such as include_thoughts stay.
var googleThinkingEffortFields = []string{"thinking_budget", "thinkingBudget", "thinking_level", "thinkingLevel"}

// clientRequestKey keys the client's request as it was before preparation
// forced the primary attempt's overrides onto it, so a failover attempt is
// rebuilt from the client's request with its own overrides.
type clientRequestKey struct{}

func withClientRequest[Req any](ctx context.Context, req Req) context.Context {
	return context.WithValue(ctx, clientRequestKey{}, req)
}

// withRequestOverrides wraps one provider call so each attempt, primary or
// failover, carries the reasoning effort and request parameters the operator
// forced for the model it goes to. A primary attempt prepared with its
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
// parameters applied and then the reasoning effort forced. The effort goes
// last so the typed override keeps winning over anything the parameter sets
// carry.
func ChatRequestWithOverrides(req *core.ChatRequest, overrides requestparams.Overrides) (*core.ChatRequest, error) {
	applied, err := requestparams.ApplyChat(req, overrides.Defaults, overrides.Params)
	if err != nil {
		return nil, err
	}
	if overrides.ReasoningEffort != "" {
		applied = ChatRequestWithReasoningEffort(applied, overrides.ReasoningEffort)
	}
	return applied, nil
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
	return applied, nil
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
