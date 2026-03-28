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
