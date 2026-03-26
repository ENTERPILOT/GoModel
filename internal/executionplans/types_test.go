package executionplans

import "testing"

func TestNormalizeScope_RejectsColonDelimitedFields(t *testing.T) {
	t.Parallel()

	tests := []Scope{
		{Provider: "openai:beta"},
		{Provider: "openai", Model: "gpt:5"},
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
