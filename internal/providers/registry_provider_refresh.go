package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

type providerRefreshTarget struct {
	provider     core.Provider
	providerName string
	providerType string
}

// RefreshProviderModels refreshes model inventory for a configured provider
// name, or all providers matching a provider type. It is intended for
// request-time recovery when startup discovery failed before a provider was
// reachable.
func (r *ModelRegistry) RefreshProviderModels(ctx context.Context, providerSelector string) (int, error) {
	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return 0, core.NewInvalidRequestError("provider selector is required", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	release, err := r.acquireRefresh(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	targets := r.providerRefreshTargets(providerSelector)
	if len(targets) == 0 {
		return 0, core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider: %s", providerSelector), nil)
	}

	targets, err = r.availableProviderRefreshTargets(ctx, providerSelector, targets)
	if err != nil {
		return 0, err
	}
	if len(targets) == 0 {
		return 0, core.NewProviderError(providerSelector, http.StatusServiceUnavailable, "provider is unavailable", nil)
	}

	configuredProviderModels, configuredProviderModelsMode := r.snapshotConfiguredProviderModels()
	providers := make([]core.Provider, 0, len(targets))
	providerTypes := make(map[core.Provider]string, len(targets))
	providerNames := make(map[core.Provider]string, len(targets))
	for _, target := range targets {
		providers = append(providers, target.provider)
		providerTypes[target.provider] = target.providerType
		providerNames[target.provider] = target.providerName
	}

	fetched := r.fetchAllProviderModels(
		ctx,
		providers,
		providerTypes,
		providerNames,
		configuredProviderModels,
		configuredProviderModelsMode,
	)

	if fetched.totalModels == 0 {
		if fetched.failedProviders == len(providers) {
			r.applyProviderRuntimeUpdates(fetched.runtimeUpdates)
			return 0, core.NewProviderError(providerSelector, http.StatusServiceUnavailable, "failed to refresh provider models", fetchedProviderRefreshError(fetched))
		}
		r.applyFetchedProviderInventory(providerTypes, fetched)
		return 0, core.NewProviderError(providerSelector, http.StatusServiceUnavailable, "provider returned no models", nil)
	}

	r.applyFetchedProviderInventory(providerTypes, fetched)
	return fetched.totalModels, nil
}

func (r *ModelRegistry) providerRefreshTargets(providerSelector string) []providerRefreshTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return nil
	}

	for _, provider := range r.providers {
		providerName := strings.TrimSpace(r.providerNames[provider])
		if providerName != providerSelector {
			continue
		}
		return []providerRefreshTarget{{
			provider:     provider,
			providerName: providerName,
			providerType: strings.TrimSpace(r.providerTypes[provider]),
		}}
	}

	targets := make([]providerRefreshTarget, 0)
	for _, provider := range r.providers {
		providerType := strings.TrimSpace(r.providerTypes[provider])
		if providerType != providerSelector {
			continue
		}
		providerName := strings.TrimSpace(r.providerNames[provider])
		if providerName == "" {
			continue
		}
		targets = append(targets, providerRefreshTarget{
			provider:     provider,
			providerName: providerName,
			providerType: providerType,
		})
	}
	return targets
}

func (r *ModelRegistry) availableProviderRefreshTargets(ctx context.Context, providerSelector string, targets []providerRefreshTarget) ([]providerRefreshTarget, error) {
	available := make([]providerRefreshTarget, 0, len(targets))
	var availabilityErrs []error

	for _, target := range targets {
		checker, ok := target.provider.(core.AvailabilityChecker)
		if !ok {
			available = append(available, target)
			continue
		}
		err := checker.CheckAvailability(ctx)
		r.RecordAvailabilityCheck(target.providerName, err)
		if err != nil {
			availabilityErrs = append(availabilityErrs, fmt.Errorf("%s: %w", target.providerName, err))
			slog.Warn("provider unavailable during request-time refresh",
				"provider", target.providerName,
				"type", target.providerType,
				"error", err,
			)
			continue
		}
		available = append(available, target)
	}

	if len(available) > 0 || len(availabilityErrs) == 0 {
		return available, nil
	}

	err := errors.Join(availabilityErrs...)
	return nil, core.NewProviderError(providerSelector, http.StatusServiceUnavailable, "provider is unavailable", err)
}

func fetchedProviderRefreshError(fetched fetchedInventory) error {
	if len(fetched.runtimeUpdates) == 0 {
		return nil
	}
	errs := make([]error, 0, len(fetched.runtimeUpdates))
	for providerName, state := range fetched.runtimeUpdates {
		errMessage := strings.TrimSpace(state.lastModelFetchError)
		if errMessage == "" {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %s", providerName, errMessage))
	}
	return errors.Join(errs...)
}

func (r *ModelRegistry) applyFetchedProviderInventory(providerTypes map[core.Provider]string, fetched fetchedInventory) {
	metadataStats := r.enrichFetchedProviderModelMaps(providerTypes, fetched.modelsByProvider)

	r.mu.Lock()
	for providerName, providerModels := range fetched.modelsByProvider {
		r.modelsByProvider[providerName] = providerModels
	}
	r.applyProviderRuntimeUpdatesLocked(fetched.runtimeUpdates)
	r.models = rebuildGlobalModelMap(r.modelsByProvider, r.providerOrderNamesLocked())
	r.invalidateSortedCaches()
	r.mu.Unlock()

	r.initMu.Lock()
	r.initialized = true
	r.initMu.Unlock()

	attrs := []any{
		"total_models", fetched.totalModels,
		"providers", len(fetched.modelsByProvider),
		"failed_providers", fetched.failedProviders,
	}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Info("provider models refreshed", attrs...)
}

func (r *ModelRegistry) providerOrderNamesLocked() []string {
	names := make([]string, 0, len(r.providers))
	for _, provider := range r.providers {
		providerName := strings.TrimSpace(r.providerNames[provider])
		if providerName == "" {
			continue
		}
		names = append(names, providerName)
	}
	return names
}
