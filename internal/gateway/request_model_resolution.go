package gateway

import (
	"context"
	"strings"

	"gomodel/internal/core"
)

type modelCountProvider interface {
	ModelCount() int
}

type providerModelRefresher interface {
	RefreshProviderModels(ctx context.Context, providerSelector string) (int, error)
}

type modelRefreshTargetResolver interface {
	ResolveRefreshTarget(requested core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// ResolvedProviderName returns the configured provider instance name for a selector.
func ResolvedProviderName(provider core.RoutableProvider, selector core.ModelSelector, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if provider == nil {
		return fallback
	}
	if named, ok := provider.(core.ProviderNameResolver); ok {
		if providerName := strings.TrimSpace(named.GetProviderName(selector.QualifiedModel())); providerName != "" {
			return providerName
		}
	}
	return fallback
}

// ResolvedWorkflowProviderName returns the configured provider name recorded in a resolution.
func ResolvedWorkflowProviderName(resolution *core.RequestModelResolution) string {
	if resolution == nil {
		return ""
	}
	if providerName := strings.TrimSpace(resolution.ProviderName); providerName != "" {
		return providerName
	}
	return strings.TrimSpace(resolution.ResolvedSelector.Provider)
}

// WorkflowProviderNameForType maps a provider type to its configured provider name when available.
func WorkflowProviderNameForType(provider core.RoutableProvider, providerType string) string {
	providerType = strings.TrimSpace(providerType)
	if providerType == "" || provider == nil {
		return ""
	}
	if named, ok := provider.(core.ProviderTypeNameResolver); ok {
		return strings.TrimSpace(named.GetProviderNameForType(providerType))
	}
	return ""
}

// ResolveRequestModel resolves a requested selector into a concrete provider/model selector.
func ResolveRequestModel(provider core.RoutableProvider, resolver ModelResolver, requested core.RequestedModelSelector) (*core.RequestModelResolution, error) {
	return ResolveRequestModelWithAuthorizer(context.Background(), provider, resolver, nil, requested)
}

// ResolveRequestModelWithAuthorizer resolves and validates a requested selector.
func ResolveRequestModelWithAuthorizer(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver ModelResolver,
	authorizer ModelAuthorizer,
	requested core.RequestedModelSelector,
) (*core.RequestModelResolution, error) {
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)

	resolvedSelector, aliasApplied, err := ResolveExecutionSelector(provider, resolver, requested)
	refreshed := false
	if err != nil {
		var refreshErr error
		refreshed, refreshErr = refreshProviderModelsForResolution(ctx, provider, resolver, requested, resolvedSelector)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if !refreshed {
			return nil, core.NewInvalidRequestError(err.Error(), err)
		}
		resolvedSelector, aliasApplied, err = ResolveExecutionSelector(provider, resolver, requested)
		if err != nil {
			return nil, core.NewInvalidRequestError(err.Error(), err)
		}
	}
	if resolvedSelector == (core.ModelSelector{}) {
		resolvedSelector, err = requested.Normalize()
		if err != nil {
			return nil, core.NewInvalidRequestError(err.Error(), err)
		}
	}

	resolvedModel := resolvedSelector.QualifiedModel()
	if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
		if !refreshed {
			var refreshErr error
			refreshed, refreshErr = refreshProviderModelsForResolution(ctx, provider, resolver, requested, resolvedSelector)
			if refreshErr != nil {
				return nil, refreshErr
			}
			if refreshed {
				resolvedSelector, aliasApplied, err = ResolveExecutionSelector(provider, resolver, requested)
				if err != nil {
					return nil, core.NewInvalidRequestError(err.Error(), err)
				}
				resolvedModel = resolvedSelector.QualifiedModel()
			}
		}
	}
	if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
		return nil, core.NewProviderError("", 0, "model registry not initialized", nil)
	}
	if !provider.Supports(resolvedModel) {
		if !refreshed {
			var refreshErr error
			refreshed, refreshErr = refreshProviderModelsForResolution(ctx, provider, resolver, requested, resolvedSelector)
			if refreshErr != nil {
				return nil, refreshErr
			}
			if refreshed {
				resolvedSelector, aliasApplied, err = ResolveExecutionSelector(provider, resolver, requested)
				if err != nil {
					return nil, core.NewInvalidRequestError(err.Error(), err)
				}
				resolvedModel = resolvedSelector.QualifiedModel()
			}
		}
	}
	if !provider.Supports(resolvedModel) {
		return nil, core.NewModelNotFoundError(resolvedModel)
	}
	if authorizer != nil {
		if err := authorizer.ValidateModelAccess(ctx, resolvedSelector); err != nil {
			return nil, err
		}
	}

	return &core.RequestModelResolution{
		Requested:        requested,
		ResolvedSelector: resolvedSelector,
		ProviderType:     strings.TrimSpace(provider.GetProviderType(resolvedModel)),
		ProviderName:     ResolvedProviderName(provider, resolvedSelector, ""),
		AliasApplied:     aliasApplied,
	}, nil
}

func refreshProviderModelsForResolution(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver ModelResolver,
	requested core.RequestedModelSelector,
	resolvedSelector core.ModelSelector,
) (bool, error) {
	refresher, ok := provider.(providerModelRefresher)
	if !ok {
		return false, nil
	}

	providerSelector := strings.TrimSpace(resolvedSelector.Provider)
	if providerSelector == "" {
		if targetResolver, ok := resolver.(modelRefreshTargetResolver); ok {
			selector, ok, err := targetResolver.ResolveRefreshTarget(requested)
			if err != nil {
				return false, err
			}
			if ok {
				providerSelector = strings.TrimSpace(selector.Provider)
			}
		}
	}
	if providerSelector == "" {
		selector, err := requested.Normalize()
		if err != nil {
			return false, nil
		}
		providerSelector = strings.TrimSpace(selector.Provider)
	}
	if providerSelector == "" {
		return false, nil
	}

	_, err := refresher.RefreshProviderModels(ctx, providerSelector)
	return true, err
}

// ResolveExecutionSelector applies explicit and provider-owned selector resolution.
func ResolveExecutionSelector(
	provider core.RoutableProvider,
	resolver ModelResolver,
	requested core.RequestedModelSelector,
) (core.ModelSelector, bool, error) {
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)

	var (
		resolvedSelector core.ModelSelector
		aliasApplied     bool
		err              error
	)

	if resolver != nil {
		resolvedSelector, aliasApplied, err = resolver.ResolveModel(requested)
		if err != nil {
			return core.ModelSelector{}, false, err
		}
		requested = core.NewRequestedModelSelector(resolvedSelector.QualifiedModel(), "")
	}

	if providerResolver, ok := provider.(ModelResolver); ok {
		providerSelector, providerChanged, err := providerResolver.ResolveModel(requested)
		if err != nil {
			if resolvedSelector != (core.ModelSelector{}) {
				// Preserve alias targets so callers can refresh the concrete provider before retrying.
				return resolvedSelector, aliasApplied, err
			}
			return core.ModelSelector{}, false, err
		}
		return providerSelector, aliasApplied || providerChanged, nil
	}

	if resolvedSelector != (core.ModelSelector{}) {
		return resolvedSelector, aliasApplied, nil
	}

	resolvedSelector, err = requested.Normalize()
	return resolvedSelector, aliasApplied, err
}
