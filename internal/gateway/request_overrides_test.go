package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// effortResolverStub forces a reasoning effort per concrete attempt model.
type effortResolverStub struct {
	efforts   map[string]string
	params    map[string]requestparams.Set
	defaults  map[string]requestparams.Set
	requested []core.RequestedModelSelector
}

func (r *effortResolverStub) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	selector, err := requested.Normalize()
	return selector, false, err
}

func (r *effortResolverStub) ResolveRequestOverrides(_ context.Context, resolution *core.RequestModelResolution, attempt core.ModelSelector) requestparams.Overrides {
	r.requested = append(r.requested, resolution.Requested)
	overrides := requestparams.Overrides{ReasoningEffort: r.efforts[attempt.QualifiedModel()]}
	if r.params != nil {
		overrides.Params = r.params[attempt.QualifiedModel()]
	}
	if r.defaults != nil {
		overrides.Defaults = r.defaults[attempt.QualifiedModel()]
	}
	return overrides
}

// effortCaptureProvider fails chat and Responses calls to "openai/primary"
// and records every request it receives.
type effortCaptureProvider struct {
	providerTypeResolverStub
	chats     []*core.ChatRequest
	responses []*core.ResponsesRequest
}

func (p *effortCaptureProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	p.chats = append(p.chats, req)
	if req.Model == "primary" {
		return nil, core.NewProviderError("openai", http.StatusInternalServerError, "primary down", nil)
	}
	return &core.ChatResponse{Model: req.Model, Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}}}, nil
}

func (p *effortCaptureProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	p.responses = append(p.responses, req)
	if req.Model == "primary" {
		return nil, core.NewProviderError("openai", http.StatusInternalServerError, "primary down", nil)
	}
	return &core.ResponsesResponse{Model: req.Model}, nil
}

func effortFixture() (*InferenceOrchestrator, *effortCaptureProvider, *effortResolverStub, *core.Workflow) {
	provider := &effortCaptureProvider{}
	resolver := &effortResolverStub{efforts: map[string]string{
		"openai/primary": "medium",
		"openai/backup":  "low",
	}}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider:      provider,
		ModelResolver: resolver,
		FailoverResolver: failoverResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
			return []core.ModelSelector{{Provider: "openai", Model: "backup"}}
		}),
	})
	workflow := &core.Workflow{
		Endpoint: core.DescribeEndpoint("POST", "/v1/chat/completions"),
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("team-model", ""),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "primary"},
			AliasApplied:     true,
		},
		Policy: &core.ResolvedWorkflowPolicy{Features: core.WorkflowFeatures{Failover: true}},
	}
	return orchestrator, provider, resolver, workflow
}

func TestChatReasoningEffortOverridePerAttempt(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	original := &core.ChatRequest{
		Model:       "primary",
		Provider:    "openai",
		Reasoning:   &core.Reasoning{Effort: "high"},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`), "seed": json.RawMessage(`7`)}),
	}

	_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, original)
	require.NoError(t, err)

	require.Len(t, provider.chats, 2)
	for i, want := range []string{"medium", "low"} {
		sent := provider.chats[i]
		require.NotNil(t, sent.Reasoning)
		assert.Equal(t, want, sent.Reasoning.Effort, "attempt %d", i)
		assert.Nil(t, sent.ExtraFields.Lookup("reasoning_effort"), "attempt %d keeps the client's flat effort", i)
		assert.JSONEq(t, `7`, string(sent.ExtraFields.Lookup("seed")), "attempt %d", i)
	}
	assert.Equal(t, "high", original.Reasoning.Effort, "client request must not be mutated")
	assert.JSONEq(t, `"high"`, string(original.ExtraFields.Lookup("reasoning_effort")))
	for _, requested := range resolver.requested {
		assert.Equal(t, "team-model", requested.Model)
	}
}

func TestResponsesReasoningEffortOverridePerAttempt(t *testing.T) {
	orchestrator, provider, _, workflow := effortFixture()
	workflow.Endpoint = core.DescribeEndpoint("POST", "/v1/responses")

	_, _, err := orchestrator.DispatchResponses(context.Background(), workflow, &core.ResponsesRequest{Model: "primary", Provider: "openai", Input: "hi"})
	require.NoError(t, err)

	require.Len(t, provider.responses, 2)
	for i, want := range []string{"medium", "low"} {
		require.NotNil(t, provider.responses[i].Reasoning)
		assert.Equal(t, want, provider.responses[i].Reasoning.Effort, "attempt %d", i)
	}
}

func TestReasoningEffortOverrideAbsentKeepsRequest(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	resolver.efforts = nil
	original := &core.ChatRequest{Model: "backup", Provider: "openai", Reasoning: &core.Reasoning{Effort: "high"}}

	_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, original)
	require.NoError(t, err)

	require.Len(t, provider.chats, 1)
	assert.Same(t, original, provider.chats[0])
}

func TestReasoningEffortOverrideForPrimary(t *testing.T) {
	orchestrator, _, _, workflow := effortFixture()
	assert.Equal(t, "medium", orchestrator.RequestOverrides(context.Background(), workflow).ReasoningEffort)

	workflow.Resolution.ResolvedSelector = core.ModelSelector{Provider: "openai", Model: "other"}
	assert.True(t, orchestrator.RequestOverrides(context.Background(), workflow).IsZero())

	assert.True(t, NewInferenceOrchestrator(InferenceConfig{}).RequestOverrides(context.Background(), workflow).IsZero())
}

// The prepared request must carry the forced effort, since the response cache
// keys on it: two aliases forcing different efforts on one model must not
// share an entry.
func TestPrepareChatRequestAppliesReasoningEffortOverride(t *testing.T) {
	orchestrator, _, _, workflow := effortFixture()
	workflow.Mode = core.ExecutionModeTranslated
	original := &core.ChatRequest{Model: "team-model", Reasoning: &core.Reasoning{Effort: "high"}}

	prepared, err := orchestrator.PrepareChatRequest(context.Background(), original, RequestMeta{
		Endpoint: workflow.Endpoint,
		Workflow: workflow,
	})
	require.NoError(t, err)

	require.NotNil(t, prepared.Request.Reasoning)
	assert.Equal(t, "medium", prepared.Request.Reasoning.Effort)
	assert.Equal(t, "primary", prepared.Request.Model)
}

func TestReasoningEffortOverrideFailoverRestoresClientEffort(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	delete(resolver.efforts, "openai/backup")
	clientExtra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)})
	prepared, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
		Model:       "primary",
		Provider:    "openai",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		Reasoning:   &core.Reasoning{Effort: "high"},
		ExtraFields: clientExtra,
	}, RequestMeta{Workflow: workflow})
	require.NoError(t, err)

	_, _, err = orchestrator.DispatchChatCompletion(prepared.Context, prepared.Workflow, prepared.Request)
	require.NoError(t, err)

	require.Len(t, provider.chats, 2)
	assert.Equal(t, "medium", provider.chats[0].Reasoning.Effort)
	require.NotNil(t, provider.chats[1].Reasoning)
	assert.Equal(t, "high", provider.chats[1].Reasoning.Effort, "fallback without an override keeps the client's effort")
	assert.JSONEq(t, `"high"`, string(provider.chats[1].ExtraFields.Lookup("reasoning_effort")))
}

func TestReasoningEffortOverrideDropsGoogleThinkingEffort(t *testing.T) {
	tests := []struct {
		name      string
		extraBody string
		want      string
	}{
		{name: "budget only drops extra_body", extraBody: `{"google":{"thinking_config":{"thinking_budget":24576}}}`},
		{name: "keeps include_thoughts", extraBody: `{"google":{"thinking_config":{"thinking_level":"high","include_thoughts":true}}}`, want: `{"google":{"thinking_config":{"include_thoughts":true}}}`},
		{name: "keeps other members", extraBody: `{"google":{"thinking_config":{"thinkingBudget":1},"cached_content":"c"},"x":1}`, want: `{"google":{"cached_content":"c"},"x":1}`},
		{name: "unrelated extra_body unchanged", extraBody: `{"google":{"cached_content":"c"}}`, want: `{"google":{"cached_content":"c"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{Model: "m", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"extra_body": json.RawMessage(tt.extraBody),
				"seed":       json.RawMessage(`7`),
			})}

			for _, fields := range []core.UnknownJSONFields{
				ChatRequestWithReasoningEffort(req, "none").ExtraFields,
				ResponsesRequestWithReasoningEffort(&core.ResponsesRequest{ExtraFields: req.ExtraFields}, "none").ExtraFields,
			} {
				assert.JSONEq(t, `7`, string(fields.Lookup("seed")))
				if tt.want == "" {
					assert.Nil(t, fields.Lookup("extra_body"))
					continue
				}
				assert.JSONEq(t, tt.want, string(fields.Lookup("extra_body")))
			}
			assert.JSONEq(t, tt.extraBody, string(req.ExtraFields.Lookup("extra_body")), "client request must not be mutated")
		})
	}
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func TestChatRequestParamsPerAttempt(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	resolver.efforts = nil
	resolver.params = map[string]requestparams.Set{
		"openai/primary": {"chat_template_kwargs": rawJSON(`{"enable_thinking":true}`), "top_k": rawJSON("20")},
		"openai/backup":  {"top_k": nil},
	}
	resolver.defaults = map[string]requestparams.Set{
		"openai/primary": {"temperature": rawJSON("1")},
		"openai/backup":  {"temperature": rawJSON("0.5")},
	}
	temperature := 0.2
	original := &core.ChatRequest{
		Model:       "primary",
		Provider:    "openai",
		Temperature: &temperature,
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"top_k": rawJSON("5"), "seed": rawJSON("7")}),
	}

	_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, original)
	require.NoError(t, err)

	require.Len(t, provider.chats, 2)
	first := provider.chats[0]
	require.NotNil(t, first.Temperature)
	assert.Equal(t, 0.2, *first.Temperature, "a default never replaces the client's value")
	assert.JSONEq(t, `20`, string(first.ExtraFields.Lookup("top_k")))
	assert.JSONEq(t, `{"enable_thinking":true}`, string(first.ExtraFields.Lookup("chat_template_kwargs")))
	assert.JSONEq(t, `7`, string(first.ExtraFields.Lookup("seed")))

	second := provider.chats[1]
	assert.Nil(t, second.ExtraFields.Lookup("top_k"), "the fallback strips top_k")
	assert.Nil(t, second.ExtraFields.Lookup("chat_template_kwargs"), "the primary's params do not leak into the fallback")
	assert.JSONEq(t, `7`, string(second.ExtraFields.Lookup("seed")))

	assert.Equal(t, 0.2, *original.Temperature, "client request must not be mutated")
	assert.JSONEq(t, `5`, string(original.ExtraFields.Lookup("top_k")))
}

func TestRequestParamsFailoverRebuildsFromClientRequest(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	resolver.efforts = nil
	resolver.params = map[string]requestparams.Set{"openai/primary": {"temperature": rawJSON("1"), "top_k": rawJSON("20")}}
	temperature := 0.2
	prepared, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
		Model:       "primary",
		Provider:    "openai",
		Temperature: &temperature,
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
	}, RequestMeta{Workflow: workflow})
	require.NoError(t, err)
	require.NotNil(t, prepared.Request.Temperature)
	assert.Equal(t, float64(1), *prepared.Request.Temperature, "the prepared request carries the forced value for the cache key")
	assert.JSONEq(t, `20`, string(prepared.Request.ExtraFields.Lookup("top_k")))

	_, _, err = orchestrator.DispatchChatCompletion(prepared.Context, prepared.Workflow, prepared.Request)
	require.NoError(t, err)

	require.Len(t, provider.chats, 2)
	assert.Same(t, prepared.Request, provider.chats[0], "the prepared primary attempt is sent as is")
	fallback := provider.chats[1]
	assert.Equal(t, "backup", fallback.Model)
	require.NotNil(t, fallback.Temperature)
	assert.Equal(t, 0.2, *fallback.Temperature, "the fallback keeps the client's temperature")
	assert.Nil(t, fallback.ExtraFields.Lookup("top_k"))
	assert.Equal(t, prepared.Request.Messages, fallback.Messages)
}

func TestResponsesRequestParamsPerAttempt(t *testing.T) {
	orchestrator, provider, resolver, workflow := effortFixture()
	workflow.Endpoint = core.DescribeEndpoint("POST", "/v1/responses")
	resolver.params = map[string]requestparams.Set{"openai/backup": {"max_tokens": rawJSON("64")}}

	_, _, err := orchestrator.DispatchResponses(context.Background(), workflow, &core.ResponsesRequest{Model: "primary", Provider: "openai", Input: "hi"})
	require.NoError(t, err)

	require.Len(t, provider.responses, 2)
	assert.Nil(t, provider.responses[0].MaxOutputTokens)
	assert.Equal(t, "medium", provider.responses[0].Reasoning.Effort)
	require.NotNil(t, provider.responses[1].MaxOutputTokens)
	assert.Equal(t, 64, *provider.responses[1].MaxOutputTokens, "max_tokens lands on max_output_tokens")
	assert.Equal(t, "low", provider.responses[1].Reasoning.Effort, "effort and params apply together")
}

func TestRequestParamsApplyErrorIsAGatewayFault(t *testing.T) {
	orchestrator, _, resolver, workflow := effortFixture()
	resolver.efforts = nil
	broken := requestparams.Set{"temperature": rawJSON(`"hot"`)}
	resolver.params = map[string]requestparams.Set{"openai/primary": broken, "openai/backup": broken}

	_, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "primary", Provider: "openai"})
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusInternalServerError, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeInternal, gatewayErr.Type)
}
