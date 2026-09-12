package workflows

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/stretchr/testify/require"
)

func systemPromptGuardrail(name string) guardrails.Definition {
	return guardrails.Definition{Name: name, Type: "system_prompt", Config: []byte(`{"mode":"inject","content":"be precise"}`)}
}

func TestCompilerCompile_Guardrails(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("policy-system"))

	compiled, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 3,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true},
			Guardrails: []GuardrailStep{
				{Ref: "policy-system", Step: 20},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, compiled)
	require.NotNil(t, compiled.Chains)
	require.Equal(t, 1, compiled.Chains.Prompt.Len())
	require.True(t, compiled.Chains.Response.Empty(), "chains = %+v, want one prompt step from the v1 payload", compiled.Chains)
	require.NotNil(t, compiled.Policy)
	require.NotEmpty(t, compiled.Policy.GuardrailsHash)
	require.Equal(t, compiled.Policy.GuardrailsHash, compiled.Policy.ChainHashes["prompt"])
	require.Len(t, compiled.Policy.ChainHashes, 1)
}

func TestCompilerCompile_PhasesFromV2Steps(t *testing.T) {
	registry := newGuardrailService(t, guardrailExecutorFunc(func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
		return &core.ChatResponse{}, nil
	}), guardrails.Definition{Name: "privacy", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o-mini"}`)})

	compiled, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-2", Name: "global",
		Payload: Payload{
			SchemaVersion: 2,
			Features:      FeatureFlags{Guardrails: true},
			Steps: []Step{
				{Ref: "privacy", Phase: PhasePrompt, Step: 10},
				{Ref: "privacy", Phase: PhaseResponse, Step: 10},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, compiled.Chains.Prompt.Len())
	require.Equal(t, 1, compiled.Chains.Response.Len())
	require.True(t, compiled.Chains.Stream.Empty(), "chains = %+v", compiled.Chains)
	require.Len(t, compiled.Policy.ChainHashes, 2)

	_, err = NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-3", Name: "global",
		Payload: Payload{
			SchemaVersion: 2,
			Features:      FeatureFlags{Guardrails: true},
			Steps:         []Step{{Ref: "privacy", Phase: PhaseStream, Step: 10}},
		},
	})
	require.ErrorContains(t, err, "does not support the stream phase")
}

func TestCompilerCompile_AppliesProcessFeatureCaps(t *testing.T) {
	failoverEnabled := true
	compiled, err := NewCompilerWithFeatureCaps(nil, core.WorkflowFeatures{
		Cache:      false,
		Audit:      true,
		Usage:      false,
		Guardrails: false,
		Failover:   false,
	}).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: true, Failover: &failoverEnabled},
			Guardrails: []GuardrailStep{
				{Ref: "policy-system", Step: 10},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, compiled)
	require.NotNil(t, compiled.Policy)
	require.False(t, compiled.Policy.Features.Cache)
	require.True(t, compiled.Policy.Features.Audit)
	require.False(t, compiled.Policy.Features.Usage)
	require.False(t, compiled.Policy.Features.Guardrails)
	require.False(t, compiled.Policy.Features.Failover)
	require.Nil(t, compiled.Chains)
	require.Empty(t, compiled.Policy.GuardrailsHash)
	require.Nil(t, compiled.Policy.ChainHashes)
}

func TestCompilerCompile_DefaultsFailoverEnabledWhenUnset(t *testing.T) {
	compiled, err := NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()).Compile(Version{
		ID:      "workflow-1",
		Scope:   Scope{},
		Version: 1,
		Name:    "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.True(t, compiled.Policy.Features.Failover)
}

func TestCompilerCompile_RejectsGuardrailsWithoutRegistry(t *testing.T) {
	_, err := NewCompilerWithFeatureCaps(nil, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "policy-system", Step: 10}},
		},
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, 502, gatewayErr.HTTPStatusCode())

	_, err = NewCompilerWithFeatureCaps(newGuardrailService(t, nil), core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "policy-system", Step: 10}},
		},
	})
	require.ErrorAs(t, err, &gatewayErr)
	require.ErrorContains(t, err, "no guardrails are loaded")
}

func TestCompilerCompile_WrapsBuildChainsErrorsAsGatewayErrors(t *testing.T) {
	registry := newGuardrailService(t, nil, systemPromptGuardrail("present"))
	_, err := NewCompilerWithFeatureCaps(registry, core.DefaultWorkflowFeatures()).Compile(Version{
		ID: "workflow-1", Name: "global",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Guardrails: true},
			Guardrails:    []GuardrailStep{{Ref: "missing", Step: 10}},
		},
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, 502, gatewayErr.HTTPStatusCode())
	require.ErrorContains(t, err, "unknown guardrail ref")
}
