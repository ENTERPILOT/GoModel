package workflows

import "testing"

func TestNormalizeScope_RejectsColonDelimitedFields(t *testing.T) {
	t.Parallel()

	tests := []Scope{
		{Provider: "openai:beta"},
		{Provider: "openai", Model: "gpt:5"},
		{UserPath: "/team:a"},
	}

	for _, scope := range tests {
		scope := scope
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

func TestFeatureFlagsRuntimeFeatures_FallbackDefaultsToTrue(t *testing.T) {
	features := FeatureFlags{
		Cache:      true,
		Audit:      true,
		Usage:      true,
		Guardrails: false,
	}.runtimeFeatures()

	if !features.Fallback {
		t.Fatal("runtimeFeatures().Fallback = false, want true")
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
			name:   "usage enabled",
			flags:  FeatureFlags{Usage: true, Budget: &explicitBudget},
			budget: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			features := tt.flags.runtimeFeatures()
			if features.Budget != tt.budget {
				t.Fatalf("runtimeFeatures().Budget = %v, want %v", features.Budget, tt.budget)
			}
		})
	}
}

func TestNormalizePayload_CanonicalizesFallbackForStableWorkflowHash(t *testing.T) {
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
			Fallback:   &explicitTrue,
		},
	})
	if err != nil {
		t.Fatalf("normalizePayload() error = %v", err)
	}

	if implicitPayload.Features.Fallback == nil || !*implicitPayload.Features.Fallback {
		t.Fatalf("implicit payload fallback = %v, want explicit true", implicitPayload.Features.Fallback)
	}
	if explicitPayload.Features.Fallback == nil || !*explicitPayload.Features.Fallback {
		t.Fatalf("explicit payload fallback = %v, want explicit true", explicitPayload.Features.Fallback)
	}
	if implicitHash != explicitHash {
		t.Fatalf("workflow hash mismatch: implicit=%q explicit=%q", implicitHash, explicitHash)
	}
}
