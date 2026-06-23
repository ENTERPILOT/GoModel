package virtualmodels

import (
	"fmt"
	"sort"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/modelselectors"
	"gomodel/internal/validation"
)

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// normalizeRedirect trims a redirect virtual model and validates the v1
// single-target constraint, returning the normalized row and parsed target.
func normalizeRedirect(vm VirtualModel) (VirtualModel, core.ModelSelector, error) {
	vm.Source = strings.TrimSpace(vm.Source)
	vm.Description = strings.TrimSpace(vm.Description)
	vm.Strategy = strings.TrimSpace(vm.Strategy)

	if vm.Source == "" {
		return VirtualModel{}, core.ModelSelector{}, newValidationError("source is required", nil)
	}
	if vm.Strategy != "" || len(vm.Targets) > 1 {
		return VirtualModel{}, core.ModelSelector{}, newValidationError("multi-target redirects (load balancing) are not yet supported", nil)
	}

	target := vm.Targets[0]
	target.Provider = strings.TrimSpace(target.Provider)
	target.Model = strings.TrimSpace(target.Model)
	if target.Model == "" {
		return VirtualModel{}, core.ModelSelector{}, newValidationError("target_model is required", nil)
	}
	vm.Targets = []Target{target}

	selector, err := target.selector()
	if err != nil {
		return VirtualModel{}, core.ModelSelector{}, newValidationError("invalid target selector: "+err.Error(), err)
	}

	// Redirects now enforce user_paths (scoped redirects), so an invalid path
	// must fail loudly rather than be silently dropped — dropping it would widen
	// the redirect to every caller.
	paths, err := normalizeUserPaths(vm.UserPaths)
	if err != nil {
		return VirtualModel{}, core.ModelSelector{}, err
	}
	vm.UserPaths = paths
	return vm, selector, nil
}

// normalizePolicyInput trims a policy virtual model and normalizes its selector
// and user paths from user-supplied input.
func normalizePolicyInput(catalog Catalog, vm VirtualModel) (VirtualModel, error) {
	parts, err := modelselectors.NormalizeInput(catalog, vm.Source)
	if err != nil {
		return VirtualModel{}, err
	}
	vm.Source = parts.Selector
	vm.ProviderName = parts.ProviderName
	vm.Model = parts.Model

	paths, err := normalizeUserPaths(vm.UserPaths)
	if err != nil {
		return VirtualModel{}, err
	}
	// Empty user_paths is allowed now: a policy with no paths means "all paths".
	vm.UserPaths = paths
	return vm, nil
}

// normalizeStoredPolicy normalizes a policy row loaded from storage.
func normalizeStoredPolicy(vm VirtualModel) (VirtualModel, error) {
	parts, err := modelselectors.NormalizeStored(vm.Source, vm.ProviderName, vm.Model)
	if err != nil {
		return VirtualModel{}, err
	}
	vm.Source = parts.Selector
	vm.ProviderName = parts.ProviderName
	vm.Model = parts.Model

	paths, err := normalizeUserPaths(vm.UserPaths)
	if err != nil {
		return VirtualModel{}, err
	}
	vm.UserPaths = paths
	return vm, nil
}

// normalizeUserPaths dedupes, normalizes, and sorts user paths. Empty input is
// allowed and yields nil.
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

func scopeKindFor(selector, providerName, model string) modelselectors.ScopeKind {
	return modelselectors.ScopeKindFor(selector, providerName, model)
}

func crossKindError(source string, wantRedirect bool) error {
	other := "an access policy"
	if !wantRedirect {
		other = "an alias"
	}
	return newValidationError(fmt.Sprintf("source %q is already used by %s", source, other), nil)
}
