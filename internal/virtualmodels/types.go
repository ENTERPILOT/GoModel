// Package virtualmodels unifies model aliases (redirects) and model access
// overrides (policies) behind one entity, the virtual model, persisted in a
// single virtual_models table.
//
// A row with Targets is a REDIRECT: Source is a new addressable name that
// rewrites to one or more real models. A redirect with a single target is a
// plain alias; a redirect with several targets is load balanced, distributing
// requests across them by Strategy (round robin or lowest cost). A row without
// Targets is an ACCESS POLICY: Source is a scoped selector over existing
// models, gated by UserPaths.
//
// The Service is a single native engine: it operates directly on VirtualModel
// rows behind one in-memory snapshot, serving both redirect resolution and
// policy authorization without composing other engines.
package virtualmodels

import (
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// Target is one concrete (provider, model) destination of a redirect.
//
// Weight biases the round-robin strategy: a target with weight 2 receives twice
// the share of a target with weight 1. A non-positive or unset weight is treated
// as 1, so single-target and unweighted redirects behave identically to before.
// Weight is ignored by the cost strategy, which always picks the cheapest target.
type Target struct {
	Provider string  `json:"provider,omitempty" bson:"provider,omitempty"`
	Model    string  `json:"model" bson:"model"`
	Weight   float64 `json:"weight,omitempty" bson:"weight,omitempty"`
}

// selector returns the concrete selector this target points to.
func (t Target) selector() (core.ModelSelector, error) {
	return core.ParseModelSelector(t.Model, t.Provider)
}

// VirtualModel is one operator-defined model entry.
type VirtualModel struct {
	Source       string    `json:"source" bson:"_id"`
	Targets      []Target  `json:"targets,omitempty" bson:"targets,omitempty"`
	Strategy     string    `json:"strategy,omitempty" bson:"strategy,omitempty"`
	ProviderName string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model        string    `json:"model,omitempty" bson:"model,omitempty"`
	UserPaths    []string  `json:"user_paths,omitempty" bson:"user_paths,omitempty"`
	Description  string    `json:"description,omitempty" bson:"description,omitempty"`
	Enabled      bool      `json:"enabled" bson:"enabled"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`

	// Managed marks a virtual model supplied declaratively through config.yaml or
	// the VIRTUAL_MODELS env var rather than the admin store. It is an in-memory
	// flag only: stores never read or write it. Managed rows override store rows
	// of the same Source and are read-only to the admin API.
	Managed bool `json:"managed,omitempty" bson:"-"`
}

// Load-balancing strategies for multi-target redirects.
const (
	// StrategyRoundRobin rotates across targets, honoring per-target Weight. It is
	// the default when Strategy is empty.
	StrategyRoundRobin = "round_robin"
	// StrategyCost always routes to the cheapest currently-available target, ranked
	// by the model registry's per-token pricing.
	StrategyCost = "cost"
)

// normalizeStrategy lower-cases and defaults a strategy string. An empty value
// defaults to round robin so single-target aliases keep their prior behavior.
func normalizeStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return StrategyRoundRobin
	}
	return strategy
}

// validStrategy reports whether strategy names a supported load-balancing mode.
func validStrategy(strategy string) bool {
	switch normalizeStrategy(strategy) {
	case StrategyRoundRobin, StrategyCost:
		return true
	default:
		return false
	}
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

// clone returns a deep copy of the virtual model so snapshot consumers cannot
// mutate cached slices.
func (v VirtualModel) clone() VirtualModel {
	if len(v.Targets) > 0 {
		v.Targets = append([]Target(nil), v.Targets...)
	}
	if len(v.UserPaths) > 0 {
		v.UserPaths = append([]string(nil), v.UserPaths...)
	}
	return v
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
	Managed       bool      `json:"managed,omitempty"`
	ResolvedModel string    `json:"resolved_model,omitempty"`
	ProviderType  string    `json:"provider_type,omitempty"`
	Valid         bool      `json:"valid,omitempty"`
	ScopeKind     string    `json:"scope_kind,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Resolution captures the requested selector and the concrete selector chosen
// after redirect resolution. Source is the redirect name that matched, if any.
type Resolution struct {
	Requested core.ModelSelector
	Resolved  core.ModelSelector
	Source    string
}

// EffectiveState is the compiled access decision for one concrete selector.
type EffectiveState struct {
	Selector       string   `json:"selector"`
	ProviderName   string   `json:"provider_name,omitempty"`
	Model          string   `json:"model,omitempty"`
	DefaultEnabled bool     `json:"default_enabled"`
	Enabled        bool     `json:"enabled"`
	UserPaths      []string `json:"user_paths,omitempty"`
}

// Catalog is the combined catalog surface the native engine needs.
type Catalog interface {
	Supports(model string) bool
	// ModelAvailable is Supports narrowed to providers whose inventory is
	// fresh: target selection uses it so load balancing routes around a
	// provider whose latest model refresh failed.
	ModelAvailable(model string) bool
	GetProviderType(model string) string
	LookupModel(model string) (*core.Model, bool)
	ProviderNames() []string
}
