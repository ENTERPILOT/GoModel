// Package virtualmodels unifies model aliases (redirects) and model access
// overrides (policies) behind one entity, the virtual model, persisted in a
// single virtual_models table.
//
// A row with Targets is a REDIRECT: Source is a new addressable name that
// rewrites to a real model (one target today; many targets later enable load
// balancing). A row without Targets is an ACCESS POLICY: Source is a scoped
// selector over existing models, gated by UserPaths.
//
// To keep v1 behavior identical to today and avoid re-porting subtle resolution
// logic, the unified service composes the existing aliases and modeloverrides
// services, feeding each from this single store via role-partitioned adapters.
package virtualmodels

import (
	"time"

	"gomodel/internal/aliases"
	"gomodel/internal/core"
	"gomodel/internal/modeloverrides"
)

// Target is one concrete (provider, model) destination of a redirect.
type Target struct {
	Provider string  `json:"provider,omitempty" bson:"provider,omitempty"`
	Model    string  `json:"model" bson:"model"`
	Weight   float64 `json:"weight,omitempty" bson:"weight,omitempty"` // inert in v1 (load balancing)
}

// VirtualModel is one operator-defined model entry.
type VirtualModel struct {
	Source       string    `json:"source" bson:"_id"`
	Targets      []Target  `json:"targets,omitempty" bson:"targets,omitempty"`
	Strategy     string    `json:"strategy,omitempty" bson:"strategy,omitempty"` // inert in v1
	ProviderName string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model        string    `json:"model,omitempty" bson:"model,omitempty"`
	UserPaths    []string  `json:"user_paths,omitempty" bson:"user_paths,omitempty"`
	Description  string    `json:"description,omitempty" bson:"description,omitempty"`
	Enabled      bool      `json:"enabled" bson:"enabled"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// IsRedirect reports whether this row redirects (has at least one target).
func (v VirtualModel) IsRedirect() bool { return len(v.Targets) > 0 }

// Kind returns the derived role: "redirect" or "policy".
func (v VirtualModel) Kind() string {
	if v.IsRedirect() {
		return KindRedirect
	}
	return KindPolicy
}

// Role kinds for the admin view.
const (
	KindRedirect = "redirect"
	KindPolicy   = "policy"
)

// View is the admin-facing representation of one virtual model.
type View struct {
	Source        string    `json:"source"`
	Kind          string    `json:"kind"`
	Targets       []Target  `json:"targets,omitempty"`
	Strategy      string    `json:"strategy,omitempty"`
	ProviderName  string    `json:"provider_name,omitempty"`
	Model         string    `json:"model,omitempty"`
	UserPaths     []string  `json:"user_paths,omitempty"`
	Description   string    `json:"description,omitempty"`
	Enabled       bool      `json:"enabled"`
	ResolvedModel string    `json:"resolved_model,omitempty"`
	ProviderType  string    `json:"provider_type,omitempty"`
	Valid         bool      `json:"valid,omitempty"`
	ScopeKind     string    `json:"scope_kind,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// vmFromAlias projects an alias as a redirect virtual model.
func vmFromAlias(a aliases.Alias) VirtualModel {
	return VirtualModel{
		Source:      a.Name,
		Targets:     []Target{{Provider: a.TargetProvider, Model: a.TargetModel}},
		Description: a.Description,
		Enabled:     a.Enabled,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

// toAlias projects a redirect virtual model back to an alias. v1 uses the first
// target; multi-target redirects (load balancing) are a later feature.
func (v VirtualModel) toAlias() aliases.Alias {
	var t Target
	if len(v.Targets) > 0 {
		t = v.Targets[0]
	}
	return aliases.Alias{
		Name:           v.Source,
		TargetModel:    t.Model,
		TargetProvider: t.Provider,
		Description:    v.Description,
		Enabled:        v.Enabled,
		CreatedAt:      v.CreatedAt,
		UpdatedAt:      v.UpdatedAt,
	}
}

// vmFromOverride projects an access override as a policy virtual model.
func vmFromOverride(o modeloverrides.Override) VirtualModel {
	return VirtualModel{
		Source:       o.Selector,
		ProviderName: o.ProviderName,
		Model:        o.Model,
		UserPaths:    append([]string(nil), o.UserPaths...),
		Enabled:      true,
		CreatedAt:    o.CreatedAt,
		UpdatedAt:    o.UpdatedAt,
	}
}

// toOverride projects a policy virtual model back to an access override.
func (v VirtualModel) toOverride() modeloverrides.Override {
	return modeloverrides.Override{
		Selector:     v.Source,
		ProviderName: v.ProviderName,
		Model:        v.Model,
		UserPaths:    append([]string(nil), v.UserPaths...),
		CreatedAt:    v.CreatedAt,
		UpdatedAt:    v.UpdatedAt,
	}
}

// Catalog is the combined catalog surface the composed services need.
type Catalog interface {
	Supports(model string) bool
	GetProviderType(model string) string
	LookupModel(model string) (*core.Model, bool)
	ProviderNames() []string
}
