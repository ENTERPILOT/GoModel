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

// guardrailSelectorStub is a workflow policy resolver that offers the listed
// guardrails on request and records what a request selected.
type guardrailSelectorStub struct {
	offered  []string
	selected [][]string
}

func (s *guardrailSelectorStub) Match(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
	return &core.ResolvedWorkflowPolicy{VersionID: "v1", Features: core.DefaultWorkflowFeatures()}, nil
}

func (s *guardrailSelectorStub) SelectRequestGuardrails(workflow *core.Workflow, names []string) error {
	s.selected = append(s.selected, names)
	for _, name := range names {
		offered := false
		for _, candidate := range s.offered {
			offered = offered || candidate == name
		}
		if !offered {
			return core.NewRequestGuardrailUnavailableError(name)
		}
	}
	policy := *workflow.Policy
	policy.GuardrailsHash = "selected"
	workflow.Policy = &policy
	workflow.RequestGuardrails = names
	return nil
}

func guardrailFixture(resolver WorkflowPolicyResolver) (*InferenceOrchestrator, *core.Workflow) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider:               &effortCaptureProvider{},
		ModelResolver:          &effortResolverStub{},
		WorkflowPolicyResolver: resolver,
	})
	workflow := &core.Workflow{
		Mode:     core.ExecutionModeTranslated,
		Endpoint: core.DescribeEndpoint("POST", "/v1/chat/completions"),
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("primary", "openai"),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "primary"},
		},
		Policy: &core.ResolvedWorkflowPolicy{VersionID: "v1", Features: core.DefaultWorkflowFeatures()},
	}
	return orchestrator, workflow
}

func guardrailFields(t *testing.T, value string) core.UnknownJSONFields {
	t.Helper()
	return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"guardrails": json.RawMessage(value),
		"stop":       json.RawMessage(`["x"]`),
	})
}

func TestPrepareChatRequestSelectsAndStripsRequestGuardrails(t *testing.T) {
	resolver := &guardrailSelectorStub{offered: []string{"pii", "judge"}}
	orchestrator, workflow := guardrailFixture(resolver)

	prepared, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
		Model:       "primary",
		Provider:    "openai",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: guardrailFields(t, `["pii","judge"]`),
	}, RequestMeta{Endpoint: workflow.Endpoint, Workflow: workflow})
	require.NoError(t, err)

	assert.Equal(t, [][]string{{"pii", "judge"}}, resolver.selected)
	assert.Equal(t, []string{"pii", "judge"}, prepared.Workflow.RequestGuardrails)
	assert.Nil(t, prepared.Request.ExtraFields.Lookup("guardrails"), "the field must not reach a provider")
	assert.JSONEq(t, `["x"]`, string(prepared.Request.ExtraFields.Lookup("stop")))
	assert.Equal(t, "selected", core.GetGuardrailsHash(prepared.Context), "the response cache keys on the selected chain")
}

func TestPrepareResponsesRequestSelectsAndStripsRequestGuardrails(t *testing.T) {
	resolver := &guardrailSelectorStub{offered: []string{"pii"}}
	orchestrator, workflow := guardrailFixture(resolver)
	workflow.Endpoint = core.DescribeEndpoint("POST", "/v1/responses")

	prepared, err := orchestrator.PrepareResponsesRequest(context.Background(), &core.ResponsesRequest{
		Model:       "primary",
		Provider:    "openai",
		Input:       "hi",
		ExtraFields: guardrailFields(t, `["pii"]`),
	}, RequestMeta{Endpoint: workflow.Endpoint, Workflow: workflow})
	require.NoError(t, err)

	assert.Equal(t, []string{"pii"}, prepared.Workflow.RequestGuardrails)
	assert.Nil(t, prepared.Request.ExtraFields.Lookup("guardrails"))
	assert.JSONEq(t, `["x"]`, string(prepared.Request.ExtraFields.Lookup("stop")))
}

func TestPrepareChatRequestRejectsRequestGuardrailsThatCannotRun(t *testing.T) {
	tests := []struct {
		name     string
		resolver WorkflowPolicyResolver
		value    string
		want     string
	}{
		{"not offered by the workflow", &guardrailSelectorStub{offered: []string{"pii"}}, `["pii","missing"]`, `guardrail "missing" cannot be selected for this request`},
		{"resolver cannot select guardrails", workflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
			return nil, nil
		}), `["pii"]`, `guardrail "pii" cannot be selected for this request`},
		{"no workflow resolver", nil, `["pii"]`, `guardrail "pii" cannot be selected for this request`},
		{"malformed field", &guardrailSelectorStub{offered: []string{"pii"}}, `"pii"`, "guardrails must be an array of guardrail names"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator, workflow := guardrailFixture(tt.resolver)

			_, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
				Model:       "primary",
				Provider:    "openai",
				Messages:    []core.Message{{Role: "user", Content: "hi"}},
				ExtraFields: guardrailFields(t, tt.value),
			}, RequestMeta{Endpoint: workflow.Endpoint, Workflow: workflow})

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Equal(t, tt.want, gatewayErr.Message)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, "guardrails", *gatewayErr.Param)
			assert.Empty(t, workflow.RequestGuardrails)
		})
	}
}

// A request without the field never asks the resolver to select anything.
func TestPrepareChatRequestWithoutRequestGuardrailsLeavesTheWorkflowAlone(t *testing.T) {
	resolver := &guardrailSelectorStub{}
	orchestrator, workflow := guardrailFixture(resolver)
	policy := workflow.Policy

	for _, fields := range []core.UnknownJSONFields{
		{},
		core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"guardrails": json.RawMessage(`[]`)}),
		core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"guardrails": json.RawMessage(`null`)}),
	} {
		prepared, err := orchestrator.PrepareChatRequest(context.Background(), &core.ChatRequest{
			Model:       "primary",
			Provider:    "openai",
			Messages:    []core.Message{{Role: "user", Content: "hi"}},
			ExtraFields: fields,
		}, RequestMeta{Endpoint: workflow.Endpoint, Workflow: workflow})
		require.NoError(t, err)
		assert.Nil(t, prepared.Request.ExtraFields.Lookup("guardrails"))
	}

	assert.Empty(t, resolver.selected)
	assert.Same(t, policy, workflow.Policy)
}
