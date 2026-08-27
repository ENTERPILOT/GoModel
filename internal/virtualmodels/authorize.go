package virtualmodels

import (
	"context"
	"net/http"
	"slices"

	"github.com/enterpilot/gomodel/internal/core"
)

// EnabledByDefault reports the process-wide model availability default.
func (s *Service) EnabledByDefault() bool {
	if s == nil {
		return true
	}
	return s.defaultEnabled
}

// EffectiveState resolves the compiled access state for one concrete selector.
func (s *Service) EffectiveState(selector core.ModelSelector) EffectiveState {
	return s.snapshot().effectiveState(selector)
}

// SetGroupResolver installs the function that resolves the group memberships
// carried by a user path (the users registry). Must be called before the
// service starts resolving requests. Without a resolver, group-scoped
// policies admit nobody through their groups.
func (s *Service) SetGroupResolver(resolver func(userPath string) []string) {
	if s == nil {
		return
	}
	s.groupResolver = resolver
}

// AllowsModel reports whether selector is available for the effective request user path.
func (s *Service) AllowsModel(ctx context.Context, selector core.ModelSelector) bool {
	state := s.EffectiveState(selector)
	if !state.Enabled {
		return false
	}
	return s.identityAllowed(core.UserPathFromContext(ctx), state.UserPaths, state.Groups)
}

// ValidateModelAccess returns a typed request error when selector is not available.
func (s *Service) ValidateModelAccess(ctx context.Context, selector core.ModelSelector) error {
	state := s.EffectiveState(selector)
	if !state.Enabled {
		return core.NewInvalidRequestErrorWithStatus(
			http.StatusBadRequest,
			"requested model is not available",
			nil,
		).WithCode("model_access_denied")
	}
	if s.identityAllowed(core.UserPathFromContext(ctx), state.UserPaths, state.Groups) {
		return nil
	}
	return core.NewInvalidRequestErrorWithStatus(
		http.StatusBadRequest,
		"requested model is not available for this API key",
		nil,
	).WithCode("model_access_denied")
}

// FilterPublicModels removes models that are unavailable for the effective request user path.
func (s *Service) FilterPublicModels(ctx context.Context, models []core.Model) []core.Model {
	if s == nil || len(models) == 0 {
		return models
	}
	result := make([]core.Model, 0, len(models))
	for _, model := range models {
		selector, err := core.ParseModelSelector(model.ID, "")
		if err != nil {
			continue
		}
		if !s.AllowsModel(ctx, selector) {
			continue
		}
		result = append(result, model)
	}
	return result
}

// identityAllowed applies the combined path/group scope of a policy: an empty
// scope admits everyone; otherwise the caller passes by user path subtree OR
// by group membership resolved from its user path.
func (s *Service) identityAllowed(userPath string, allowedPaths, allowedGroups []string) bool {
	if len(allowedPaths) == 0 && len(allowedGroups) == 0 {
		return true
	}
	if len(allowedPaths) > 0 && userPathAllowed(userPath, allowedPaths) {
		return true
	}
	return len(allowedGroups) > 0 && s.groupAllowed(userPath, allowedGroups)
}

// groupAllowed reports whether the user path carries one of the allowed
// groups. allowed must be sorted (normalizeGroups sorts).
func (s *Service) groupAllowed(userPath string, allowed []string) bool {
	if s == nil || s.groupResolver == nil || len(allowed) == 0 {
		return false
	}
	for _, group := range s.groupResolver(userPath) {
		if _, ok := slices.BinarySearch(allowed, group); ok {
			return true
		}
	}
	return false
}

func userPathAllowed(userPath string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	if _, ok := slices.BinarySearch(allowed, "/"); ok {
		return true
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return false
	}
	ancestors := core.UserPathAncestors(userPath)
	for _, candidate := range ancestors {
		if _, ok := slices.BinarySearch(allowed, candidate); ok {
			return true
		}
	}
	return false
}
