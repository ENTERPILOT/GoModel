package virtualmodels

import (
	"context"
	"sort"
	"strings"

	"gomodel/internal/core"
)

// Resolve resolves raw model/provider inputs through the redirect table.
func (s *Service) Resolve(model, provider string) (Resolution, bool, error) {
	return s.resolveRequested(core.NewRequestedModelSelector(model, provider), "", false)
}

func (s *Service) resolveRequested(requested core.RequestedModelSelector, userPath string, enforceUserPaths bool) (Resolution, bool, error) {
	selector, err := requested.Normalize()
	if err != nil {
		return Resolution{}, false, err
	}
	if requested.ExplicitProvider {
		return Resolution{Requested: selector, Resolved: selector}, false, nil
	}
	if resolution, ok := s.snapshot().resolveRedirect(requested.Model, s.catalog, userPath, enforceUserPaths); ok {
		return resolution, true, nil
	}
	return Resolution{Requested: selector, Resolved: selector}, false, nil
}

// ResolveModel resolves a requested selector and returns the concrete selector
// chosen for execution. It does not consult user_paths; scoped redirects are
// applied by ResolveModelForUserPath on the request path.
func (s *Service) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	resolution, changed, err := s.resolveRequested(requested, "", false)
	if err != nil {
		return core.ModelSelector{}, false, err
	}
	return resolution.Resolved, changed, nil
}

// ResolveModelForUserPath resolves a requested selector honoring per-redirect
// user_paths against the effective request user path. A redirect scoped to
// user_paths the caller does not match falls through to the literal model name.
func (s *Service) ResolveModelForUserPath(ctx context.Context, requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	resolution, changed, err := s.resolveRequested(requested, core.UserPathFromContext(ctx), true)
	if err != nil {
		return core.ModelSelector{}, false, err
	}
	return resolution.Resolved, changed, nil
}

// ResolveRefreshTarget returns a redirect target without consulting the current
// catalog so callers can refresh an unavailable target provider before normal
// resolution is retried.
func (s *Service) ResolveRefreshTarget(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if s == nil || requested.ExplicitProvider {
		return core.ModelSelector{}, false, nil
	}
	name := strings.TrimSpace(requested.Model)
	if name == "" {
		return core.ModelSelector{}, false, nil
	}
	entry, ok := s.snapshot().redirects[name]
	if !ok || !entry.vm.Enabled {
		return core.ModelSelector{}, false, nil
	}
	return entry.target, true, nil
}

// Supports reports whether a redirect currently resolves to a concrete model.
func (s *Service) Supports(model string) bool {
	_, ok := s.snapshot().resolveRedirect(model, s.catalog, "", false)
	return ok
}

// GetProviderType returns the resolved provider type for a redirect, or empty
// when unresolved.
func (s *Service) GetProviderType(model string) string {
	if resolution, ok := s.snapshot().resolveRedirect(model, s.catalog, "", false); ok {
		return strings.TrimSpace(s.catalog.GetProviderType(resolution.Resolved.QualifiedModel()))
	}
	return ""
}

// ExposedModels returns enabled redirects projected as model-list entries.
func (s *Service) ExposedModels() []core.Model {
	return s.exposedModels("", false, nil)
}

// ExposedModelsFiltered returns enabled redirects projected as model-list
// entries, filtered by the concrete target selector.
func (s *Service) ExposedModelsFiltered(allow func(core.ModelSelector) bool) []core.Model {
	return s.exposedModels("", false, allow)
}

// ExposedModelsForUserPath is ExposedModelsFiltered plus per-redirect user_path
// scoping: a redirect carrying user_paths is hidden from callers it would not
// apply to, so a scoped alias is not listed (its name exposed) to callers
// outside its scope even though resolution would fall through for them.
func (s *Service) ExposedModelsForUserPath(userPath string, allow func(core.ModelSelector) bool) []core.Model {
	return s.exposedModels(userPath, true, allow)
}

func (s *Service) exposedModels(userPath string, enforceUserPaths bool, allow func(core.ModelSelector) bool) []core.Model {
	snap := s.snapshot()
	result := make([]core.Model, 0, len(snap.order))
	for _, source := range snap.order {
		entry := snap.redirects[source]
		if !entry.vm.Enabled {
			continue
		}
		if enforceUserPaths && len(entry.vm.UserPaths) > 0 && !userPathAllowed(userPath, entry.vm.UserPaths) {
			continue
		}
		if allow != nil && !allow(entry.target) {
			continue
		}
		model, ok := s.catalog.LookupModel(entry.qualified)
		if !ok || model == nil {
			continue
		}
		cloned := *model
		cloned.ID = entry.vm.Source
		result = append(result, cloned)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
