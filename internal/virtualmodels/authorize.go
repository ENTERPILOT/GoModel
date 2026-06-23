package virtualmodels

import (
	"context"
	"net/http"
	"slices"

	"gomodel/internal/core"
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

// AllowsModel reports whether selector is available for the effective request user path.
func (s *Service) AllowsModel(ctx context.Context, selector core.ModelSelector) bool {
	state := s.EffectiveState(selector)
	if !state.Enabled {
		return false
	}
	if len(state.UserPaths) == 0 {
		return true
	}
	return userPathAllowed(core.UserPathFromContext(ctx), state.UserPaths)
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
	if len(state.UserPaths) == 0 {
		return nil
	}
	if userPathAllowed(core.UserPathFromContext(ctx), state.UserPaths) {
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
