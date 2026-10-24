package workflows

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeScope_RejectsColonDelimitedFields(t *testing.T) {
	t.Parallel()

	tests := []Scope{
		{Provider: "openai:beta"},
		{Provider: "openai", Model: "gpt:5"},
		{UserPath: "/team:a"},
	}

	for _, scope := range tests {
		t.Run(scope.Provider+"|"+scope.Model, func(t *testing.T) {
			t.Parallel()

			_, _, err := normalizeScope(scope)
			require.Error(t, err)
			require.True(t, IsValidationError(err))
		})
	}
}

func TestNormalizeScope_AllowsPathOnlyScope(t *testing.T) {
	t.Parallel()

	scope, scopeKey, err := normalizeScope(Scope{UserPath: "/team/a"})
	require.NoError(t, err)
	require.Equal(t, "/team/a", scope.UserPath)
	require.Equal(t, "path:/team/a", scopeKey)
}

func TestNormalizeCreateInput_AllowsEmptyName(t *testing.T) {
	t.Parallel()

	input, scopeKey, workflowHash, err := normalizeCreateInput(CreateInput{
		Scope:    Scope{},
		Activate: true,
		Name:     "",
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.NoError(t, err)
	require.Empty(t, input.Name)
	require.Equal(t, "global", scopeKey)
	require.NotEmpty(t, workflowHash)
}

func TestNormalizeCreateInput_RejectsReservedManagedDefaultIdentityForUserPlans(t *testing.T) {
	t.Parallel()

	_, _, _, err := normalizeCreateInput(CreateInput{
		Scope:       Scope{},
		Activate:    true,
		Name:        ManagedDefaultGlobalName,
		Description: ManagedDefaultGlobalDescription,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestNormalizeCreateInput_RejectsManagedDefaultForNonGlobalScope(t *testing.T) {
	t.Parallel()

	_, _, _, err := normalizeCreateInput(CreateInput{
		Scope:    Scope{Provider: "openai"},
		Activate: true,
		Managed:  true,
		Name:     ManagedDefaultGlobalName,
		Payload: Payload{
			SchemaVersion: 1,
			Features:      FeatureFlags{Cache: true, Audit: true, Usage: true, Guardrails: false},
		},
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestFeatureFlagsRuntimeFeatures_FailoverDefaultsToTrue(t *testing.T) {
	features := FeatureFlags{
		Cache:      true,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}.runtimeFeatures()

	require.True(t, features.Failover)
}

func TestFeatureFlagsRuntimeFeatures_DisablesBudgetWhenUsageDisabled(t *testing.T) {
	t.Parallel()

	explicitBudget := true
	tests := []struct {
		name   string
		flags  FeatureFlags
		budget bool
	}{
		{
			name:   "implicit budget",
			flags:  FeatureFlags{Usage: false},
			budget: false,
		},
		{
			name:   "explicit budget",
			flags:  FeatureFlags{Usage: false, Budget: &explicitBudget},
			budget: false,
		},
		{
			name:   "implicit budget with usage enabled",
			flags:  FeatureFlags{Usage: true},
			budget: true,
		},
		{
			name:   "explicit budget with usage enabled",
			flags:  FeatureFlags{Usage: true, Budget: &explicitBudget},
			budget: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			features := tt.flags.runtimeFeatures()
			require.Equal(t, tt.budget, features.Budget)
		})
	}
}

func TestNormalizePayload_CanonicalizesFailoverForStableWorkflowHash(t *testing.T) {
	explicitTrue := true

	implicitPayload, implicitHash, err := normalizePayload(Payload{
		SchemaVersion: 1,
		Features: FeatureFlags{
			Cache:      true,
			Audit:      true,
			Usage:      true,
			Guardrails: false,
		},
	})
	require.NoError(t, err)

	explicitPayload, explicitHash, err := normalizePayload(Payload{
		SchemaVersion: 1,
		Features: FeatureFlags{
			Cache:      true,
			Audit:      true,
			Usage:      true,
			Guardrails: false,
			Failover:   &explicitTrue,
			Budget:     &explicitTrue,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, implicitPayload.Features.Failover)
	require.True(t, *implicitPayload.Features.Failover)
	require.NotNil(t, explicitPayload.Features.Failover)
	require.True(t, *explicitPayload.Features.Failover)
	require.NotNil(t, implicitPayload.Features.Budget)
	require.True(t, *implicitPayload.Features.Budget)
	require.NotNil(t, explicitPayload.Features.Budget)
	require.True(t, *explicitPayload.Features.Budget)
	require.Equal(t, explicitHash, implicitHash)
}

func TestNormalizePayload_V2Steps(t *testing.T) {
	payload, hash, err := normalizePayload(Payload{
		SchemaVersion: 2,
		Features:      FeatureFlags{Guardrails: true},
		Guardrails:    []GuardrailStep{{Ref: "legacy", Step: 5}},
		Steps: []Step{
			{Ref: "b", Phase: "stream", Step: 10},
			{Ref: " a ", Phase: "", Step: 20},
			{Ref: "b", Phase: "Response", Step: 10},
			{Ref: "c", Phase: "prompt", Step: 10},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hash)
	require.Equal(t, 2, payload.SchemaVersion)
	require.Nil(t, payload.Guardrails)

	want := []Step{
		{Ref: "legacy", Phase: "prompt", Step: 5},
		{Ref: "c", Phase: "prompt", Step: 10},
		{Ref: "a", Phase: "prompt", Step: 20},
		{Ref: "b", Phase: "response", Step: 10},
		{Ref: "b", Phase: "stream", Step: 10},
	}
	require.Equal(t, want, payload.Steps)
	steps := payload.EffectiveSteps()
	require.Len(t, steps, 5)
}

func TestNormalizePayload_V2Validation(t *testing.T) {
	tests := []struct {
		name    string
		payload Payload
		want    string
	}{
		{"duplicate ref per phase", Payload{SchemaVersion: 2, Steps: []Step{{Ref: "a", Step: 1}, {Ref: "a", Phase: "prompt", Step: 2}}}, "duplicate guardrail ref in prompt phase"},
		{"invalid phase", Payload{SchemaVersion: 2, Steps: []Step{{Ref: "a", Phase: "route", Step: 1}}}, "invalid step phase"},
		{"empty ref", Payload{SchemaVersion: 2, Steps: []Step{{Ref: " ", Step: 1}}}, "ref is required"},
		{"negative step", Payload{SchemaVersion: 2, Steps: []Step{{Ref: "a", Step: -1}}}, "step must not be negative"},
		{"negative legacy step", Payload{SchemaVersion: 1, Guardrails: []GuardrailStep{{Ref: "a", Step: -1}}}, "step must not be negative"},
		{"negative folded legacy step", Payload{SchemaVersion: 2, Guardrails: []GuardrailStep{{Ref: "a", Step: -5}}}, "step must not be negative"},
		{"v1 with steps", Payload{SchemaVersion: 1, Steps: []Step{{Ref: "a", Step: 1}}}, "schema_version 1"},
		{"unsupported version", Payload{SchemaVersion: 3}, "unsupported schema_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizePayload(tt.payload)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestNormalizePayload_DefaultsSchemaVersion(t *testing.T) {
	legacy, _, err := normalizePayload(Payload{Guardrails: []GuardrailStep{{Ref: "a", Step: 1}}})
	require.NoError(t, err)
	require.Equal(t, 1, legacy.SchemaVersion)
	require.Len(t, legacy.Guardrails, 1, "legacy = %+v, %v", legacy, err)
	steps := legacy.EffectiveSteps()
	require.Len(t, steps, 1)
	require.Equal(t, PhasePrompt, steps[0].Phase)

	fresh, _, err := normalizePayload(Payload{})
	require.NoError(t, err)
	require.Equal(t, 2, fresh.SchemaVersion, "fresh = %+v, %v", fresh, err)
}
