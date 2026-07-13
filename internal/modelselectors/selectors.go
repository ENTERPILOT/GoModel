// Package modelselectors normalizes provider/model selector strings shared by
// model-level admin features.
package modelselectors

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/validation"
)

// Catalog is the minimal configured-provider surface needed for selector validation.
type Catalog interface {
	ProviderNames() []string
}

// Selector is the normalized form of a model selector.
type Selector struct {
	Selector     string
	ProviderName string
	Model        string
}

// ScopeKind identifies how broadly a selector applies.
type ScopeKind string

const (
	ScopeGlobal        ScopeKind = "global"
	ScopeModel         ScopeKind = "model"
	ScopeProvider      ScopeKind = "provider"
	ScopeProviderModel ScopeKind = "provider_model"
)

// ValidationError indicates invalid selector input or invalid selector state.
type ValidationError = validation.Error

// NewValidationError creates a selector validation error.
func NewValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

// NormalizeInput validates and normalizes one user-supplied selector.
func NormalizeInput(catalog Catalog, raw string) (Selector, error) {
	return NormalizeInputWithProviderNames(ProviderNames(catalog), raw)
}

// NormalizeInputWithProviderNames validates and normalizes one user-supplied selector.
func NormalizeInputWithProviderNames(providerNames []string, raw string) (Selector, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Selector{}, NewValidationError("selector is required", nil)
	}
	if IsGlobal(raw) {
		return Selector{Selector: "/"}, nil
	}

	providerNameSet := make(map[string]struct{}, len(providerNames))
	for _, name := range providerNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		providerNameSet[name] = struct{}{}
	}

	var providerName, model string
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
		return Selector{}, NewValidationError("selector is required", nil)
	}
	if providerName != "" {
		if _, exists := providerNameSet[providerName]; !exists {
			return Selector{}, NewValidationError("unknown provider_name: "+providerName, nil)
		}
	}
	return Selector{
		Selector:     String(providerName, model),
		ProviderName: providerName,
		Model:        model,
	}, nil
}

// NormalizeStored normalizes selector columns loaded from storage.
func NormalizeStored(selector, providerName, model string) (Selector, error) {
	selector = strings.TrimSpace(selector)
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	globalSelector := IsGlobal(selector)

	if selector == "" {
		selector = String(providerName, model)
	}
	if selector == "" {
		return Selector{}, NewValidationError("selector is required", nil)
	}
	if providerName == "" && model == "" && !globalSelector {
		providerName, model = ParseStoredParts(selector)
	}
	if providerName == "" && model == "" && !globalSelector {
		return Selector{}, NewValidationError("selector is required", nil)
	}
	if normalized := String(providerName, model); normalized != "" {
		selector = normalized
	}
	return Selector{
		Selector:     selector,
		ProviderName: providerName,
		Model:        model,
	}, nil
}

// ProviderNames returns a copy of configured provider names from catalog.
func ProviderNames(catalog Catalog) []string {
	if catalog == nil {
		return nil
	}
	return append([]string(nil), catalog.ProviderNames()...)
}

// ScopeKindFor reports the normalized selector scope.
func ScopeKindFor(selector, providerName, model string) ScopeKind {
	switch {
	case IsGlobal(selector):
		return ScopeGlobal
	case strings.TrimSpace(providerName) != "" && strings.TrimSpace(model) != "":
		return ScopeProviderModel
	case strings.TrimSpace(providerName) != "":
		return ScopeProvider
	default:
		return ScopeModel
	}
}

// String returns the canonical selector string for normalized parts.
func String(providerName, model string) string {
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

// IsGlobal reports whether raw selects every provider and model.
func IsGlobal(raw string) bool {
	return strings.TrimSpace(raw) == "/"
}

// ExactMatchKey returns the lookup key used to index exact provider+model overrides.
// Returns "" when either side is missing — exact matching only applies to fully-qualified pairs.
func ExactMatchKey(providerName, model string) string {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return ""
	}
	return providerName + "/" + model
}

// ParseStoredParts splits a stored selector without a configured-provider catalog.
// This is a best-effort fallback for old rows. Stores should persist
// provider_name and model columns because slash-shaped model IDs cannot be
// distinguished from provider/model selectors without a provider catalog.
func ParseStoredParts(selector string) (providerName, model string) {
	selector = strings.TrimSpace(selector)
	if IsGlobal(selector) {
		return "", ""
	}
	if providerName, model, ok := splitFirst(selector); ok {
		return providerName, model
	}
	return "", selector
}

func splitFirst(value string) (prefix, rest string, ok bool) {
	prefix, rest, ok = strings.Cut(value, "/")
	if !ok {
		return "", "", false
	}
	prefix = strings.TrimSpace(prefix)
	rest = strings.TrimSpace(rest)
	return prefix, rest, true
}
