package modeloverrides

import (
	"sort"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/modelselectors"
)

// Override stores one persisted access-policy override for a model selector.
//
// Selector syntax:
//   - /
//   - model
//   - provider/model
//   - provider/
//
// The first slash separates provider name from model. When the prefix is not a
// configured provider name, the full value is treated as a raw model ID. The
// bare slash selects every configured provider and model.
type Override struct {
	Selector     string    `json:"selector" bson:"_id"`
	ProviderName string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model        string    `json:"model,omitempty" bson:"model,omitempty"`
	UserPaths    []string  `json:"user_paths,omitempty" bson:"user_paths,omitempty"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// ScopeKind identifies how broadly an override applies.
type ScopeKind = modelselectors.ScopeKind

// ScopeKind reports the normalized selector scope for one override.
func (o Override) ScopeKind() ScopeKind {
	return modelselectors.ScopeKindFor(o.Selector, o.ProviderName, o.Model)
}

// View is the admin-facing representation of one persisted override.
type View struct {
	Override
	ScopeKind ScopeKind `json:"scope_kind"`
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

// Catalog is the minimal configured-provider surface needed for selector validation.
type Catalog interface {
	ProviderNames() []string
}

func normalizeOverrideInput(catalog Catalog, override Override) (Override, error) {
	parts, err := modelselectors.NormalizeInput(catalog, override.Selector)
	if err != nil {
		return Override{}, err
	}

	override.Selector = parts.Selector
	override.ProviderName = parts.ProviderName
	override.Model = parts.Model

	paths, err := normalizeUserPaths(override.UserPaths)
	if err != nil {
		return Override{}, err
	}
	if len(paths) == 0 {
		return Override{}, newValidationError("user_paths is required", nil)
	}
	override.UserPaths = paths
	return override, nil
}

func normalizeStoredOverride(override Override) (Override, error) {
	parts, err := modelselectors.NormalizeStored(override.Selector, override.ProviderName, override.Model)
	if err != nil {
		return Override{}, err
	}
	override.Selector = parts.Selector
	override.ProviderName = parts.ProviderName
	override.Model = parts.Model

	paths, err := normalizeUserPaths(override.UserPaths)
	if err != nil {
		return Override{}, err
	}
	if len(paths) == 0 {
		return Override{}, newValidationError("user_paths is required", nil)
	}
	override.UserPaths = paths
	return override, nil
}

func normalizeSelectorInput(providerNames []string, raw string) (selector, providerName, model string, err error) {
	parts, err := modelselectors.NormalizeInputWithProviderNames(providerNames, raw)
	if err != nil {
		return "", "", "", err
	}
	return parts.Selector, parts.ProviderName, parts.Model, nil
}

func selectorProviderNames(catalog Catalog) []string {
	return modelselectors.ProviderNames(catalog)
}

func normalizeUserPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, raw := range paths {
		path, err := core.NormalizeUserPath(raw)
		if err != nil {
			return nil, newValidationError("invalid user_paths value", err)
		}
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func selectorString(providerName, model string) string {
	return modelselectors.String(providerName, model)
}
