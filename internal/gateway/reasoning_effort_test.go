package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// effortResolverStub forces a reasoning effort per concrete attempt model.
type effortResolverStub struct {
	efforts   map[string]string
	requested []core.RequestedModelSelector
}

func (r *effortResolverStub) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	selector, err := requested.Normalize()
	return selector, false, err
}

func (r *effortResolverStub) ResolveReasoningEffort(_ context.Context, requested core.RequestedModelSelector, attempt core.ModelSelector) string {
	r.requested = append(r.requested, requested)
	return r.efforts[attempt.QualifiedModel()]
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
	assert.Equal(t, "medium", orchestrator.ReasoningEffortOverride(context.Background(), workflow))

	workflow.Resolution.ResolvedSelector = core.ModelSelector{Provider: "openai", Model: "other"}
	assert.Empty(t, orchestrator.ReasoningEffortOverride(context.Background(), workflow))

	assert.Empty(t, NewInferenceOrchestrator(InferenceConfig{}).ReasoningEffortOverride(context.Background(), workflow))
}
