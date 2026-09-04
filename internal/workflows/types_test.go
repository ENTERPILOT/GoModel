package workflows

import (
	"strings"
	"testing"
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
			if err == nil {
				t.Fatal("normalizeScope() error = nil, want validation error")
			}
			if !IsValidationError(err) {
				t.Fatalf("normalizeScope() error = %T, want validation error", err)
			}
		})
	}
}

func TestNormalizeScope_AllowsPathOnlyScope(t *testing.T) {
	t.Parallel()

	scope, scopeKey, err := normalizeScope(Scope{UserPath: "/team/a"})
	if err != nil {
		t.Fatalf("normalizeScope() error = %v", err)
	}
	if scope.UserPath != "/team/a" {
		t.Fatalf("scope.UserPath = %q, want /team/a", scope.UserPath)
	}
	if scopeKey != "path:/team/a" {
		t.Fatalf("scopeKey = %q, want path:/team/a", scopeKey)
	}
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
	if err != nil {
		t.Fatalf("normalizeCreateInput() error = %v", err)
	}
	if input.Name != "" {
		t.Fatalf("Name = %q, want empty", input.Name)
	}
	if scopeKey != "global" {
		t.Fatalf("scopeKey = %q, want global", scopeKey)
	}
	if workflowHash == "" {
		t.Fatal("workflowHash is empty")
	}
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
	if err == nil {
		t.Fatal("normalizeCreateInput() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("normalizeCreateInput() error = %T, want validation error", err)
	}
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
	if err == nil {
		t.Fatal("normalizeCreateInput() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("normalizeCreateInput() error = %T, want validation error", err)
	}
}

func TestFeatureFlagsRuntimeFeatures_FailoverDefaultsToTrue(t *testing.T) {
	features := FeatureFlags{
		Cache:      true,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}.runtimeFeatures()

	if !features.Failover {
		t.Fatal("runtimeFeatures().Failover = false, want true")
	}
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
			if features.Budget != tt.budget {
				t.Fatalf("runtimeFeatures().Budget = %v, want %v", features.Budget, tt.budget)
			}
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
	if err != nil {
		t.Fatalf("normalizePayload() error = %v", err)
	}

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
	if err != nil {
		t.Fatalf("normalizePayload() error = %v", err)
	}

	if implicitPayload.Features.Failover == nil || !*implicitPayload.Features.Failover {
		t.Fatalf("implicit payload failover = %v, want explicit true", implicitPayload.Features.Failover)
	}
	if explicitPayload.Features.Failover == nil || !*explicitPayload.Features.Failover {
		t.Fatalf("explicit payload failover = %v, want explicit true", explicitPayload.Features.Failover)
	}
	if implicitPayload.Features.Budget == nil || !*implicitPayload.Features.Budget {
		t.Fatalf("implicit payload budget = %v, want explicit true", implicitPayload.Features.Budget)
	}
	if explicitPayload.Features.Budget == nil || !*explicitPayload.Features.Budget {
		t.Fatalf("explicit payload budget = %v, want explicit true", explicitPayload.Features.Budget)
	}
	if implicitHash != explicitHash {
		t.Fatalf("workflow hash mismatch: implicit=%q explicit=%q", implicitHash, explicitHash)
	}
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
	if err != nil {
		t.Fatalf("normalizePayload() error = %v", err)
	}
	if hash == "" || payload.SchemaVersion != 2 || payload.Guardrails != nil {
		t.Fatalf("payload = %+v, hash = %q", payload, hash)
	}
	want := []Step{
		{Ref: "legacy", Phase: "prompt", Step: 5},
		{Ref: "c", Phase: "prompt", Step: 10},
		{Ref: "a", Phase: "prompt", Step: 20},
		{Ref: "b", Phase: "response", Step: 10},
		{Ref: "b", Phase: "stream", Step: 10},
	}
	if len(payload.Steps) != len(want) {
		t.Fatalf("steps = %+v, want %+v", payload.Steps, want)
	}
	for i := range want {
		if payload.Steps[i] != want[i] {
			t.Fatalf("steps[%d] = %+v, want %+v", i, payload.Steps[i], want[i])
		}
	}
	if steps := payload.EffectiveSteps(); len(steps) != 5 {
		t.Fatalf("EffectiveSteps() = %+v", steps)
	}
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
		{"v1 with steps", Payload{SchemaVersion: 1, Steps: []Step{{Ref: "a", Step: 1}}}, "schema_version 1"},
		{"unsupported version", Payload{SchemaVersion: 3}, "unsupported schema_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizePayload(tt.payload)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("normalizePayload() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNormalizePayload_DefaultsSchemaVersion(t *testing.T) {
	legacy, _, err := normalizePayload(Payload{Guardrails: []GuardrailStep{{Ref: "a", Step: 1}}})
	if err != nil || legacy.SchemaVersion != 1 || len(legacy.Guardrails) != 1 {
		t.Fatalf("legacy = %+v, %v", legacy, err)
	}
	if steps := legacy.EffectiveSteps(); len(steps) != 1 || steps[0].Phase != PhasePrompt {
		t.Fatalf("EffectiveSteps() = %+v", steps)
	}
	fresh, _, err := normalizePayload(Payload{})
	if err != nil || fresh.SchemaVersion != 2 {
		t.Fatalf("fresh = %+v, %v", fresh, err)
	}
}
