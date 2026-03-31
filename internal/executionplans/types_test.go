package executionplans

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

	input, scopeKey, planHash, err := normalizeCreateInput(CreateInput{
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
	if planHash == "" {
		t.Fatal("planHash is empty")
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

func TestNormalizePayload_CanonicalizesFallbackForStablePlanHash(t *testing.T) {
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
		t.Fatalf("plan hash mismatch: implicit=%q explicit=%q", implicitHash, explicitHash)
	}
}
