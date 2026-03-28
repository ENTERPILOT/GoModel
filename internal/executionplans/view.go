package executionplans

import "gomodel/internal/core"

// View is the admin-facing representation of one active execution-plan version.
// It includes both the persisted payload and the effective runtime features after
// process-level feature caps are applied.
type View struct {
	Version
	ScopeType         string                 `json:"scope_type"`
	ScopeDisplay      string                 `json:"scope_display"`
	EffectiveFeatures core.ExecutionFeatures `json:"effective_features"`
	GuardrailsHash    string                 `json:"guardrails_hash,omitempty"`
}
