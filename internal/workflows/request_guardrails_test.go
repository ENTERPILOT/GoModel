package workflows

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
)

func replaceGuardrail(name string) guardrails.Definition {
	return guardrails.Definition{Name: name, Type: "string_replace", Config: []byte(`{"rules":"secret => [redacted]"}`)}
}

// requestGuardrailService serves one global workflow: "always" runs on every
// request, "extra" (prompt and response) and "scan" (response) on request.
func requestGuardrailService(t *testing.T, features core.WorkflowFeatures) *Service {
	t.Helper()
	registry := newGuardrailService(t, nil, systemPromptGuardrail("always"), replaceGuardrail("extra"), replaceGuardrail("scan"))
	version := globalVersionV1(FeatureFlags{Guardrails: true})
	version.Payload = Payload{
		SchemaVersion: 2,
		Features:      FeatureFlags{Guardrails: true},
		Steps: []Step{
			{Ref: "always", Phase: PhasePrompt, Step: 10},
			{Ref: "extra", Phase: PhasePrompt, Step: 20, OnRequest: true},
			{Ref: "extra", Phase: PhaseResponse, Step: 10, OnRequest: true},
			{Ref: "scan", Phase: PhaseResponse, Step: 20, OnRequest: true},
		},
	}
	service, err := NewService(&staticStore{versions: []Version{version}}, NewCompilerWithFeatureCaps(registry, features))
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))
	return service
}

func matchedWorkflow(t *testing.T, service *Service) *core.Workflow {
	t.Helper()
	policy, err := service.Match(core.WorkflowSelector{})
	require.NoError(t, err)
	return &core.Workflow{Policy: policy}
}

func chainNames(chain *plugins.Chain) []string {
	names := []string{}
	for _, inst := range chain.Instances() {
		names = append(names, inst.Name)
	}
	return names
}

func TestCompilerCompile_OnRequestStepsStayOutOfTheFixedChains(t *testing.T) {
	service := requestGuardrailService(t, core.DefaultWorkflowFeatures())
	compiled := service.snapshot().byScope[scopeRef{}]
	require.NotNil(t, compiled)

	assert.Equal(t, []string{"always"}, chainNames(compiled.Chains.Prompt))
	assert.True(t, compiled.Chains.Response.Empty(), "an on-request step must not run for a request that did not ask for it")
	assert.Equal(t, []string{"always", "extra"}, chainNames(compiled.Offered.Prompt))
	assert.Equal(t, []string{"extra", "scan"}, chainNames(compiled.Offered.Response))
	assert.Equal(t, compiled.Chains.CacheHash(), compiled.Policy.GuardrailsHash, "the workflow hash covers the fixed steps only")

	chains := service.ChainsForWorkflow(matchedWorkflow(t, service))
	assert.Same(t, compiled.Chains, chains)
}

func TestCompilerCompile_WithoutOnRequestStepsOffersNothing(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("always"))
	compiled, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{SchemaVersion: 2, Features: FeatureFlags{Guardrails: true}, Steps: []Step{{Ref: "always", Phase: PhasePrompt, Step: 10}}},
	})
	require.NoError(t, err)
	assert.Nil(t, compiled.Offered)
	assert.Equal(t, 1, compiled.Chains.Prompt.Len())
}

// Two editing guardrails cannot share a step. The check covers the on-request
// steps too, so no selection a request can make builds an invalid chain.
func TestCompilerCompile_ValidatesOnRequestStepsWithTheFixedOnes(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("always"), replaceGuardrail("extra"))
	_, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{SchemaVersion: 2, Features: FeatureFlags{Guardrails: true}, Steps: []Step{
			{Ref: "always", Phase: PhasePrompt, Step: 10},
			{Ref: "extra", Phase: PhasePrompt, Step: 10, OnRequest: true},
		}},
	})
	require.ErrorContains(t, err, "only one mutating plugin may share a step")
}

func TestServiceSelectRequestGuardrails(t *testing.T) {
	tests := []struct {
		name         string
		names        []string
		wantAdded    []string
		wantPrompt   []string
		wantResponse []string
	}{
		{"one guardrail adds its step in every phase it is offered in", []string{"extra"}, []string{"extra"}, []string{"always", "extra"}, []string{"extra"}},
		{"several guardrails", []string{"scan", "extra"}, []string{"scan", "extra"}, []string{"always", "extra"}, []string{"extra", "scan"}},
		{"a guardrail that always runs adds nothing", []string{"always"}, nil, []string{"always"}, []string{}},
		{"always-on and on-request names together", []string{"always", "scan"}, []string{"scan"}, []string{"always"}, []string{"scan"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := requestGuardrailService(t, core.DefaultWorkflowFeatures())
			workflow := matchedWorkflow(t, service)
			base := *workflow.Policy

			require.NoError(t, service.SelectRequestGuardrails(workflow, tt.names))

			assert.Equal(t, tt.wantAdded, workflow.RequestGuardrails)
			chains := service.ChainsForWorkflow(workflow)
			assert.Equal(t, tt.wantPrompt, chainNames(chains.Prompt))
			assert.Equal(t, tt.wantResponse, chainNames(chains.Response))
			if len(tt.wantAdded) == 0 {
				assert.Equal(t, base.GuardrailsHash, workflow.Policy.GuardrailsHash)
				return
			}
			assert.NotEqual(t, base.GuardrailsHash, workflow.Policy.GuardrailsHash, "the response cache must key on the chain that runs")
			assert.Equal(t, chains.CacheHash(), workflow.Policy.GuardrailsHash)
			assert.Equal(t, chains.Hashes(), workflow.Policy.ChainHashes)
		})
	}
}

// Selecting guardrails changes one request's workflow only: the compiled
// policy other requests match is left as it was.
func TestServiceSelectRequestGuardrails_LeavesTheSharedPolicyAlone(t *testing.T) {
	service := requestGuardrailService(t, core.DefaultWorkflowFeatures())
	first := matchedWorkflow(t, service)
	require.NoError(t, service.SelectRequestGuardrails(first, []string{"extra"}))

	second := matchedWorkflow(t, service)
	assert.Empty(t, second.RequestGuardrails)
	assert.NotEqual(t, first.Policy.GuardrailsHash, second.Policy.GuardrailsHash)
	assert.Equal(t, []string{"always"}, chainNames(service.ChainsForWorkflow(second).Prompt))
}

func TestServiceSelectRequestGuardrails_RejectsWhatTheWorkflowDoesNotOffer(t *testing.T) {
	tests := []struct {
		name     string
		features core.WorkflowFeatures
		workflow func(*core.Workflow)
		names    []string
		wantName string
	}{
		{"unknown name", core.DefaultWorkflowFeatures(), nil, []string{"missing"}, `"missing"`},
		{"one unknown name among offered ones", core.DefaultWorkflowFeatures(), nil, []string{"extra", "missing"}, `"missing"`},
		{"names are matched exactly", core.DefaultWorkflowFeatures(), nil, []string{"Extra"}, `"Extra"`},
		{"guardrails turned off for the gateway", core.WorkflowFeatures{}, nil, []string{"extra"}, `"extra"`},
		{"no workflow matched", core.DefaultWorkflowFeatures(), func(w *core.Workflow) { w.Policy = nil }, []string{"extra"}, `"extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := requestGuardrailService(t, tt.features)
			workflow := matchedWorkflow(t, service)
			if tt.workflow != nil {
				tt.workflow(workflow)
			}

			err := service.SelectRequestGuardrails(workflow, tt.names)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.wantName)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, "guardrails", *gatewayErr.Param)
			assert.Empty(t, workflow.RequestGuardrails, "a rejected selection must not change what runs")
		})
	}
}

func TestServiceSelectRequestGuardrails_NoNamesIsANoOp(t *testing.T) {
	service := requestGuardrailService(t, core.DefaultWorkflowFeatures())
	workflow := matchedWorkflow(t, service)
	policy := workflow.Policy

	require.NoError(t, service.SelectRequestGuardrails(workflow, nil))

	assert.Same(t, policy, workflow.Policy)
	assert.Empty(t, workflow.RequestGuardrails)
}

// A live workflow holds the instances of its on-request steps too, so the
// guardrails service does not close one a request may still add.
func TestServiceInstallHoldsOnRequestChains(t *testing.T) {
	s := &Service{}
	s.current.Store(newSnapshot())
	fixed := &plugins.Instance{Name: "fixed"}
	offered := &plugins.Instance{Name: "offered"}
	compiled := &CompiledWorkflow{
		Version: Version{ID: "v1"},
		Policy:  &core.ResolvedWorkflowPolicy{},
		Chains:  &plugins.Chains{Prompt: &plugins.Chain{Steps: []plugins.Step{{Instances: []*plugins.Instance{fixed}}}}},
		Offered: &plugins.Chains{Prompt: &plugins.Chain{Steps: []plugins.Step{{Instances: []*plugins.Instance{fixed, offered}}}}},
	}
	next := newSnapshot()
	next.byScope[scopeRef{}] = compiled
	next.byVersionID["v1"] = compiled
	s.install(next)
	require.True(t, offered.Held())

	s.install(newSnapshot())
	assert.False(t, offered.Held())
	assert.False(t, fixed.Held())
}

func TestNormalizePayload_OnRequestSteps(t *testing.T) {
	payload, hash, err := normalizePayload(Payload{
		SchemaVersion: 2,
		Steps: []Step{
			{Ref: "b", Phase: "response", Step: 10, OnRequest: true},
			{Ref: "a", Step: 10},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []Step{
		{Ref: "a", Phase: "prompt", Step: 10},
		{Ref: "b", Phase: "response", Step: 10, OnRequest: true},
	}, payload.Steps)

	_, fixedHash, err := normalizePayload(Payload{
		SchemaVersion: 2,
		Steps: []Step{
			{Ref: "b", Phase: "response", Step: 10},
			{Ref: "a", Step: 10},
		},
	})
	require.NoError(t, err)
	assert.NotEqual(t, fixedHash, hash, "making a step on-request is a new workflow version")
}

// The chains of a request that added guardrails are resolved from the live
// workflow in every phase, so a guardrail replaced while the request is in
// flight runs as its current instance in the phases still to come.
func TestServiceChainsForWorkflow_ResolvesRequestGuardrailsFromTheLiveWorkflow(t *testing.T) {
	service := requestGuardrailService(t, core.DefaultWorkflowFeatures())
	workflow := matchedWorkflow(t, service)
	require.NoError(t, service.SelectRequestGuardrails(workflow, []string{"extra"}))
	before := service.ChainsForWorkflow(workflow)

	require.NoError(t, service.Refresh(context.Background()))

	after := service.ChainsForWorkflow(workflow)
	assert.Equal(t, chainNames(before.Prompt), chainNames(after.Prompt))
	live := service.snapshot().byScope[scopeRef{}]
	assert.Same(t, live.Offered.Prompt.Instances()[1], after.Prompt.Instances()[1])
}
