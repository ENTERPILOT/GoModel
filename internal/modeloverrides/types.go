package modeloverrides

import (
	"sort"
	"strings"
	"time"

	"gomodel/internal/core"
)

// Override stores one persisted access-policy override for a model selector.
//
// Selector syntax:
//   - model
//   - provider/model
//   - provider/
//
// The first slash separates provider name from model. When the prefix is not a
// configured provider name, the full value is treated as a raw model ID.
type Override struct {
	Selector                string    `json:"selector" bson:"_id"`
	ProviderName            string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model                   string    `json:"model,omitempty" bson:"model,omitempty"`
	Enabled                 *bool     `json:"enabled,omitempty" bson:"enabled,omitempty"`
	ForceDisabled           bool      `json:"force_disabled,omitempty" bson:"force_disabled,omitempty"`
	AllowedOnlyForUserPaths []string  `json:"allowed_only_for_user_paths,omitempty" bson:"allowed_only_for_user_paths,omitempty"`
	CreatedAt               time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt               time.Time `json:"updated_at" bson:"updated_at"`
}

// ScopeKind identifies how broadly an override applies.
type ScopeKind string

const (
	ScopeModel         ScopeKind = "model"
	ScopeProvider      ScopeKind = "provider"
	ScopeProviderModel ScopeKind = "provider_model"
)

// ScopeKind reports the normalized selector scope for one override.
func (o Override) ScopeKind() ScopeKind {
	switch {
	case strings.TrimSpace(o.ProviderName) != "" && strings.TrimSpace(o.Model) != "":
		return ScopeProviderModel
	case strings.TrimSpace(o.ProviderName) != "":
		return ScopeProvider
	default:
		return ScopeModel
	}
}

// View is the admin-facing representation of one persisted override.
type View struct {
	Override
	ScopeKind ScopeKind `json:"scope_kind"`
}

// EffectiveState is the compiled access decision for one concrete selector.
type EffectiveState struct {
	Selector                string   `json:"selector"`
	ProviderName            string   `json:"provider_name,omitempty"`
	Model                   string   `json:"model,omitempty"`
	DefaultEnabled          bool     `json:"default_enabled"`
	Enabled                 bool     `json:"enabled"`
	ForceDisabled           bool     `json:"force_disabled"`
	AllowedOnlyForUserPaths []string `json:"allowed_only_for_user_paths,omitempty"`
}

// Catalog is the minimal configured-provider surface needed for selector validation.
type Catalog interface {
	ProviderNames() []string
}

func normalizeOverrideInput(catalog Catalog, override Override) (Override, error) {
	selector, providerName, model, err := normalizeSelectorInput(selectorProviderNames(catalog), override.Selector)
	if err != nil {
		return Override{}, err
	}

	override.Selector = selector
	override.ProviderName = providerName
	override.Model = model

	if override.ForceDisabled && override.Enabled != nil && *override.Enabled {
		return Override{}, newValidationError("force_disabled cannot be combined with enabled=true", nil)
	}

	paths, err := normalizeUserPaths(override.AllowedOnlyForUserPaths)
	if err != nil {
		return Override{}, err
	}
	override.AllowedOnlyForUserPaths = paths
	return override, nil
}

func normalizeStoredOverride(override Override) (Override, error) {
	override.Selector = strings.TrimSpace(override.Selector)
	override.ProviderName = strings.TrimSpace(override.ProviderName)
	override.Model = strings.TrimSpace(override.Model)

	if override.Selector == "" {
		override.Selector = selectorString(override.ProviderName, override.Model)
	}
	if override.Selector == "" {
		return Override{}, newValidationError("selector is required", nil)
	}
	if override.ProviderName == "" && override.Model == "" {
		providerName, model := parseStoredSelectorParts(override.Selector)
		override.ProviderName = providerName
		override.Model = model
	}
	if override.ProviderName == "" && override.Model == "" {
		return Override{}, newValidationError("selector is required", nil)
	}
	if normalized := selectorString(override.ProviderName, override.Model); normalized != "" {
		override.Selector = normalized
	}

	paths, err := normalizeUserPaths(override.AllowedOnlyForUserPaths)
	if err != nil {
		return Override{}, err
	}
	override.AllowedOnlyForUserPaths = paths
	return override, nil
}

func normalizeSelectorInput(providerNames []string, raw string) (selector, providerName, model string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", newValidationError("selector is required", nil)
	}

	providerNameSet := make(map[string]struct{}, len(providerNames))
	for _, name := range providerNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		providerNameSet[name] = struct{}{}
	}

	if prefix, rest, ok := splitFirst(raw); ok {
		if _, exists := providerNameSet[prefix]; exists {
			providerName = prefix
			model = rest
		} else {
			model = raw
		}
	} else {
		model = raw
	}

	if providerName == "" && model == "" {
		return "", "", "", newValidationError("selector is required", nil)
	}
	if providerName != "" {
		if _, exists := providerNameSet[providerName]; !exists {
			return "", "", "", newValidationError("unknown provider_name: "+providerName, nil)
		}
	}
	return selectorString(providerName, model), providerName, model, nil
}

func selectorProviderNames(catalog Catalog) []string {
	if catalog == nil {
		return nil
	}
	return append([]string(nil), catalog.ProviderNames()...)
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
			return nil, newValidationError("invalid allowed_only_for_user_paths value", err)
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
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	switch {
	case providerName != "" && model != "":
		return providerName + "/" + model
	case providerName != "":
		return providerName + "/"
	case model != "":
		return model
	default:
		return ""
	}
}

func exactMatchKey(providerName, model string) string {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return ""
	}
	return providerName + "/" + model
}

func splitFirst(value string) (prefix, rest string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(value), "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	prefix = strings.TrimSpace(parts[0])
	rest = strings.TrimSpace(parts[1])
	if prefix == "" {
		return "", "", false
	}
	return prefix, rest, true
}

func parseStoredSelectorParts(selector string) (providerName, model string) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", ""
	}
	if strings.HasSuffix(selector, "/") {
		return strings.TrimSpace(strings.TrimSuffix(selector, "/")), ""
	}
	if providerName, model, ok := splitFirst(selector); ok {
		return providerName, model
	}
	return "", selector
}

func cloneEnabled(value *bool) *bool {
	if value == nil {
		return nil
	}
	enabled := *value
	return &enabled
}
