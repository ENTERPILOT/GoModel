package virtualmodels

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/validation"
)

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return validation.IsError(err)
}

func newValidationError(message string, err error) error {
	return validation.NewError(message, err)
}

// normalizeRedirect trims and validates a redirect virtual model, returning the
// normalized row and the parsed selector for every target. A redirect may carry
// one target (a plain alias) or several (load balanced by Strategy).
func normalizeRedirect(vm VirtualModel) (VirtualModel, []core.ModelSelector, error) {
	vm.Source = strings.TrimSpace(vm.Source)
	vm.Description = strings.TrimSpace(vm.Description)
	vm.Strategy = normalizeStrategy(vm.Strategy)

	if vm.Source == "" {
		return VirtualModel{}, nil, newValidationError("source is required", nil)
	}
	if !validStrategy(vm.Strategy) {
		return VirtualModel{}, nil, newValidationError(
			fmt.Sprintf("unknown load-balancing strategy %q (use %q or %q)", vm.Strategy, StrategyRoundRobin, StrategyCost), nil)
	}
	if len(vm.Targets) == 0 {
		return VirtualModel{}, nil, newValidationError("at least one target is required", nil)
	}

	targets := make([]Target, 0, len(vm.Targets))
	selectors := make([]core.ModelSelector, 0, len(vm.Targets))
	seen := make(map[string]struct{}, len(vm.Targets))
	for _, target := range vm.Targets {
		target.Provider = strings.TrimSpace(target.Provider)
		target.Model = strings.TrimSpace(target.Model)
		if target.Model == "" {
			return VirtualModel{}, nil, newValidationError("target model is required", nil)
		}
		if target.Weight < 0 {
			return VirtualModel{}, nil, newValidationError("target weight cannot be negative", nil)
		}
		selector, err := target.selector()
		if err != nil {
			return VirtualModel{}, nil, newValidationError("invalid target selector: "+err.Error(), err)
		}
		qualified := selector.QualifiedModel()
		if _, dup := seen[qualified]; dup {
			return VirtualModel{}, nil, newValidationError("duplicate target: "+qualified, nil)
		}
		seen[qualified] = struct{}{}
		targets = append(targets, target)
		selectors = append(selectors, selector)
	}
	vm.Targets = targets

	// Redirects now enforce user_paths (scoped redirects), so an invalid path
	// must fail loudly rather than be silently dropped — dropping it would widen
	// the redirect to every caller.
	paths, err := normalizeUserPaths(vm.UserPaths)
	if err != nil {
		return VirtualModel{}, nil, err
	}
	vm.UserPaths = paths
	return vm, selectors, nil
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

// providerNameSet builds a trimmed membership set from provider names.
func providerNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// unknownTargetProviderError reports a target provider name that no configured
// provider matches, listing the registered names so a typo is easy to spot.
func unknownTargetProviderError(provider string, registered []string) error {
	names := make([]string, 0, len(registered))
	for _, name := range registered {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	known := "none"
	if len(names) > 0 {
		known = strings.Join(names, ", ")
	}
	return newValidationError(fmt.Sprintf("unknown target provider %q (registered providers: %s)", provider, known), nil)
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
