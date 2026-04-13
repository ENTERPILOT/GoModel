package workflows

import (
	"time"

	"gomodel/internal/core"
)

// View is the admin-facing representation of one active workflow version.
// It includes both the persisted payload and the effective runtime features after
// process-level feature caps are applied. Broken rows are still returned with
// CompileError populated so the admin API can inspect persisted workflows that
// no longer compile cleanly.
type View struct {
	ID           string    `json:"id"`
	Scope        Scope     `json:"scope"`
	Version      int       `json:"version"`
	Active       bool      `json:"active"`
	Managed      bool      `json:"managed_default,omitempty"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Payload      Payload   `json:"workflow_payload"`
	WorkflowHash string    `json:"workflow_hash"`
	CreatedAt    time.Time `json:"created_at"`

	ScopeType         string                `json:"scope_type"`
	ScopeDisplay      string                `json:"scope_display"`
	EffectiveFeatures core.WorkflowFeatures `json:"effective_features"`
	GuardrailsHash    string                `json:"guardrails_hash,omitempty"`
	CompileError      string                `json:"compile_error,omitempty"`
}

// NewViewFromVersion maps the persisted workflow version into the explicit
// admin response shape without exposing storage-only fields or tags.
func NewViewFromVersion(version Version) View {
	return View{
		ID:           version.ID,
		Scope:        version.Scope,
		Version:      version.Version,
		Active:       version.Active,
		Managed:      version.Managed,
		Name:         version.Name,
		Description:  version.Description,
		Payload:      version.Payload,
		WorkflowHash: version.WorkflowHash,
		CreatedAt:    version.CreatedAt,
	}
}
