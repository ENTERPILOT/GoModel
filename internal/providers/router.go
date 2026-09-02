// Package providers provides a router for multiple LLM providers.
package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ErrRegistryNotInitialized is returned when the router is used before the registry has any models.
var ErrRegistryNotInitialized = fmt.Errorf("model registry has no models: ensure Initialize() or LoadFromCache() is called before using the router")

// Router routes requests to the appropriate provider based on the model lookup.
// It uses a dynamic model-to-provider mapping that is populated at startup
// by fetching available models from each provider's /models endpoint.
type Router struct {
	lookup core.ModelLookup
	// caps holds the optional lookup capabilities, resolved once at
	// construction so the request path never repeats the type assertions.
	caps         lookupCaps
	cachePlanner *cachePlanner
	// unqualifiedModelIDs makes ListModels advertise bare model IDs instead of
	// provider-qualified ones.
	unqualifiedModelIDs bool
}

// lookupCaps records which optional interfaces the lookup implements. A nil
// field means the lookup lacks that capability and the router uses the
// fallback path built on the core.ModelLookup contract.
type lookupCaps struct {
	typeRegistry            providerTypeRegistry
	nameRegistry            providerNameRegistry
	initialized             initializedLookup
	typeLister              providerTypeLister
	nameLister              providerNameLister
	publicModels            publicModelLister
	unqualifiedPublicModels unqualifiedPublicModelLister
	modelsWithProvider      modelWithProviderLister
	selectorResolver        qualifiedSelectorResolver
	modelInfo               modelInfoLookup
	refresher               providerModelRefresher
}

func resolveLookupCaps(lookup core.ModelLookup) lookupCaps {
	var caps lookupCaps
	caps.typeRegistry, _ = lookup.(providerTypeRegistry)
	caps.nameRegistry, _ = lookup.(providerNameRegistry)
	caps.initialized, _ = lookup.(initializedLookup)
	caps.typeLister, _ = lookup.(providerTypeLister)
	caps.nameLister, _ = lookup.(providerNameLister)
	caps.publicModels, _ = lookup.(publicModelLister)
	caps.unqualifiedPublicModels, _ = lookup.(unqualifiedPublicModelLister)
	caps.modelsWithProvider, _ = lookup.(modelWithProviderLister)
	caps.selectorResolver, _ = lookup.(qualifiedSelectorResolver)
	caps.modelInfo, _ = lookup.(modelInfoLookup)
	caps.refresher, _ = lookup.(providerModelRefresher)
	return caps
}

type providerTypeRegistry interface {
	ProviderByType(providerType string) core.Provider
}

type providerNameRegistry interface {
	ProviderByName(providerName string) core.Provider
}

type initializedLookup interface {
	IsInitialized() bool
}

type providerTypeLister interface {
	ProviderTypes() []string
}

type providerNameLister interface {
	ProviderNames() []string
}

type publicModelLister interface {
	ListPublicModels() []core.Model
}

type unqualifiedPublicModelLister interface {
	ListUnqualifiedPublicModels() []core.Model
}

type modelWithProviderLister interface {
	ListModelsWithProvider() []ModelWithProvider
}

// qualifiedSelectorResolver is an optional fast path for qualified selector
// resolution. Implementations resolve a "<segment>/<modelID>" pair via an O(1)
// index instead of scanning the catalog. A false result means the caller should
// fall back to the slower catalog scan for raw/edge-case selectors.
type qualifiedSelectorResolver interface {
	ResolveProviderSelector(segment, modelID string) (core.ModelSelector, bool)
}

// modelInfoLookup is an optional fast path: the registry hands back provider,
// provider type, and provider name under one lock instead of one lock per
// accessor. Lookups without it fall back to GetProvider + GetProviderType.
type modelInfoLookup interface {
	GetModel(model string) *ModelInfo
}

type providerModelRefresher interface {
	RefreshProviderModels(ctx context.Context, providerSelector string) (int, error)
}

func registryUnavailableError(err error) error {
	return core.NewProviderError("", http.StatusServiceUnavailable, err.Error(), err)
}

// NewRouter creates a new provider router with a model lookup.
// The lookup must be initialized (via Initialize() or LoadFromCache()) before using the router.
// Returns an error if the lookup is nil.
func NewRouter(lookup core.ModelLookup) (*Router, error) {
	if lookup == nil {
		return nil, fmt.Errorf("lookup cannot be nil")
	}
	return &Router{
		lookup:       lookup,
		caps:         resolveLookupCaps(lookup),
		cachePlanner: newCachePlanner(),
	}, nil
}

// SetUnqualifiedModelIDs controls whether ListModels advertises bare model IDs
// (gpt-5) instead of provider-qualified ones (openai/gpt-5).
func (r *Router) SetUnqualifiedModelIDs(enabled bool) {
	r.unqualifiedModelIDs = enabled
}

// checkReady verifies the lookup has models available.
// Returns ErrRegistryNotInitialized if no models are loaded.
func (r *Router) checkReady() error {
	if r.lookup.ModelCount() == 0 {
		return ErrRegistryNotInitialized
	}
	return nil
}

// ResolveModel canonicalizes a requested selector into the concrete
// provider-name-qualified selector used for execution.
//
// Resolution precedence is:
//  1. configured provider name + model ID
//  2. provider type + model ID
//  3. raw slash-shaped model ID (only when provider was not explicit)
//  4. default normalization fallback
func (r *Router) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if err := r.checkReady(); err != nil {
		return core.ModelSelector{}, false, registryUnavailableError(err)
	}

	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)
	selector, err := requested.Normalize()
	if err != nil {
		return core.ModelSelector{}, false, core.NewInvalidRequestError(err.Error(), err)
	}

	resolved := selector
	if selector.Provider == "" {
		if concrete, ok := r.resolveUnqualifiedSelector(selector); ok {
			resolved = concrete
		}
	} else if concrete, ok := r.resolveQualifiedSelector(requested, selector); ok {
		resolved = concrete
	}

	return resolved, resolved.QualifiedModel() != selector.QualifiedModel(), nil
}

func (r *Router) resolveUnqualifiedSelector(selector core.ModelSelector) (core.ModelSelector, bool) {
	if selector.Provider != "" || selector.Model == "" {
		return core.ModelSelector{}, false
	}
	providerName := r.lookup.GetProviderName(selector.Model)
	if providerName == "" {
		return core.ModelSelector{}, false
	}
	return core.ModelSelector{Provider: providerName, Model: selector.Model}, true
}

func (r *Router) resolveQualifiedSelector(requested core.RequestedModelSelector, selector core.ModelSelector) (core.ModelSelector, bool) {
	// selector comes from Normalize(), so both segments are already trimmed.
	providerSegment := selector.Provider
	modelID := selector.Model
	if providerSegment == "" || modelID == "" {
		return core.ModelSelector{}, false
	}

	// O(1) fast path: direct provider name/type match. Falls through to the
	// catalog scan only for raw slash-shaped IDs and other edge cases.
	if r.caps.selectorResolver != nil {
		if concrete, ok := r.caps.selectorResolver.ResolveProviderSelector(providerSegment, modelID); ok {
			return concrete, true
		}
	}

	// The remaining passes scan the catalog, which needs the lister.
	if r.caps.modelsWithProvider == nil {
		return core.ModelSelector{}, false
	}

	// Fallback for lookups that don't implement qualifiedSelectorResolver (and for
	// raw slash-shaped model IDs the fast path can't key on). The parsed-modelID
	// pass mirrors the fast path for non-indexed lookups; the requested.Model pass
	// additionally resolves models whose own IDs contain a slash.
	entries := r.caps.modelsWithProvider.ListModelsWithProvider()

	if concrete, ok := resolveProviderOwnedRawSelector(entries, providerSegment, modelID); ok {
		return concrete, true
	}
	if concrete, ok := resolveProviderOwnedRawSelector(entries, providerSegment, requested.Model); ok {
		return concrete, true
	}

	if requested.ExplicitProvider {
		return core.ModelSelector{}, false
	}
	if r.hasConfiguredProviderName(providerSegment) {
		return core.ModelSelector{}, false
	}
	if r.providerByTypeRegistry(providerSegment) != nil {
		return core.ModelSelector{}, false
	}

	rawModelID := requested.Model
	if rawModelID == "" {
		return core.ModelSelector{}, false
	}
	for _, entry := range entries {
		if entry.Model.ID != rawModelID {
			continue
		}
		return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
	}

	return core.ModelSelector{}, false
}

// resolveProviderOwnedRawSelector scans the catalog for a trimmed provider
// segment (name first, then type) and a trimmed raw model ID.
func resolveProviderOwnedRawSelector(entries []ModelWithProvider, providerSegment, rawModelID string) (core.ModelSelector, bool) {
	if providerSegment == "" || rawModelID == "" {
		return core.ModelSelector{}, false
	}

	for _, entry := range entries {
		if entry.ProviderName == providerSegment && entry.Model.ID == rawModelID {
			return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
		}
	}

	for _, entry := range entries {
		if entry.ProviderType == providerSegment && entry.Model.ID == rawModelID {
			return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
		}
	}

	return core.ModelSelector{}, false
}

// hasConfiguredProviderName reports whether a trimmed provider name is a
// configured provider instance.
func (r *Router) hasConfiguredProviderName(providerName string) bool {
	if providerName == "" {
		return false
	}
	if r.caps.nameLister != nil {
		return slices.Contains(r.caps.nameLister.ProviderNames(), providerName)
	}
	if r.caps.modelsWithProvider != nil {
		for _, entry := range r.caps.modelsWithProvider.ListModelsWithProvider() {
			if entry.ProviderName == providerName {
				return true
			}
		}
	}
	return false
}

// resolvedRoute is the outcome of resolving a request's model selector: the
// provider instance to call, the concrete selector to forward, and the
// provider type used for response stamping and request planning.
type resolvedRoute struct {
	provider     core.Provider
	selector     core.ModelSelector
	providerType string
}

// lookupRoute fetches the provider and provider type for an already-resolved
// selector, taking the registry lock once when the lookup supports it.
func (r *Router) lookupRoute(model string) (core.Provider, string) {
	if r.caps.modelInfo != nil {
		info := r.caps.modelInfo.GetModel(model)
		if info == nil || info.Provider == nil {
			return nil, ""
		}
		return info.Provider, info.ProviderType
	}
	p := r.lookup.GetProvider(model)
	if p == nil {
		return nil, ""
	}
	return p, r.lookup.GetProviderType(model)
}

// resolveProvider validates readiness, parses the model selector, and finds the target provider.
func (r *Router) resolveProvider(ctx context.Context, model, providerHint string) (resolvedRoute, error) {
	requested := core.NewRequestedModelSelector(model, providerHint)
	selector, _, err := r.ResolveModel(requested)
	refreshed := false
	if err != nil {
		var refreshErr error
		refreshed, refreshErr = r.refreshProviderModelsForRequest(ctx, requested)
		if refreshErr != nil {
			return resolvedRoute{}, refreshErr
		}
		if !refreshed {
			return resolvedRoute{}, err
		}
		selector, _, err = r.ResolveModel(requested)
		if err != nil {
			return resolvedRoute{}, err
		}
	}

	lookupModel := selector.QualifiedModel()
	p, providerType := r.lookupRoute(lookupModel)
	if p == nil && !refreshed {
		var refreshErr error
		refreshed, refreshErr = r.refreshProviderModelsForRequest(ctx, requested)
		if refreshErr != nil {
			return resolvedRoute{}, refreshErr
		}
		if refreshed {
			selector, _, err = r.ResolveModel(requested)
			if err != nil {
				return resolvedRoute{}, err
			}
			lookupModel = selector.QualifiedModel()
			p, providerType = r.lookupRoute(lookupModel)
		}
	}
	if p == nil {
		return resolvedRoute{}, core.NewNotFoundError("model not found: " + lookupModel)
	}
	return resolvedRoute{provider: p, selector: selector, providerType: providerType}, nil
}

func (r *Router) refreshProviderModelsForRequest(ctx context.Context, requested core.RequestedModelSelector) (bool, error) {
	if r.caps.refresher == nil {
		return false, nil
	}

	selector, err := requested.Normalize()
	if err != nil {
		return false, nil
	}
	providerSelector := selector.Provider
	if providerSelector == "" {
		return false, nil
	}
	if !r.hasRegisteredProviderSelector(providerSelector) {
		return false, nil
	}

	_, err = r.caps.refresher.RefreshProviderModels(ctx, providerSelector)
	return true, err
}

// RefreshProviderModels refreshes a configured provider's model inventory when
// the backing lookup supports request-time provider refreshes.
func (r *Router) RefreshProviderModels(ctx context.Context, providerSelector string) (int, error) {
	providerSelector = strings.TrimSpace(providerSelector)
	if !r.hasRegisteredProviderSelector(providerSelector) {
		return 0, nil
	}
	if r.caps.refresher == nil {
		return 0, nil
	}
	return r.caps.refresher.RefreshProviderModels(ctx, providerSelector)
}

// hasRegisteredProviderSelector reports whether a trimmed selector names a
// configured provider instance or a registered provider type.
func (r *Router) hasRegisteredProviderSelector(providerSelector string) bool {
	if providerSelector == "" {
		return false
	}
	if r.hasConfiguredProviderName(providerSelector) {
		return true
	}
	if r.lookup.GetProviderNameForType(providerSelector) != "" {
		return true
	}
	return r.providerByTypeRegistry(providerSelector) != nil
}

func (r *Router) resolveProviderType(providerType string) (core.Provider, error) {
	if err := r.ensureProviderInventoryReady(); err != nil {
		return nil, err
	}
	if providerType == "" {
		return nil, core.NewInvalidRequestError("provider type is required", nil)
	}
	provider := r.providerByTypeRegistry(providerType)
	if provider == nil {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider type: %s", providerType), nil)
	}
	return provider, nil
}

func (r *Router) resolveProviderSelector(providerSelector string) (core.Provider, string, error) {
	if err := r.ensureProviderInventoryReady(); err != nil {
		return nil, "", err
	}
	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return nil, "", core.NewInvalidRequestError("provider is required", nil)
	}
	if provider := r.providerByTypeRegistry(providerSelector); provider != nil {
		return provider, providerSelector, nil
	}
	if provider := r.providerByNameRegistry(providerSelector); provider != nil {
		providerType := r.GetProviderTypeForName(providerSelector)
		if providerType == "" {
			providerType = providerSelector
		}
		return provider, providerType, nil
	}
	return nil, "", core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider: %s", providerSelector), nil)
}

func (r *Router) ensureProviderInventoryReady() error {
	if r.caps.initialized != nil {
		if !r.caps.initialized.IsInitialized() {
			if err := r.checkReady(); err != nil {
				if errors.Is(err, ErrRegistryNotInitialized) {
					return registryUnavailableError(err)
				}
				return err
			}
		}
	} else if err := r.checkReady(); err != nil {
		if errors.Is(err, ErrRegistryNotInitialized) {
			return registryUnavailableError(err)
		}
		return err
	}
	return nil
}

// Supports returns true if any provider supports the given model.
// Returns false if the lookup has no models loaded.
func (r *Router) Supports(model string) bool {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return false
	}
	return r.lookup.Supports(selector.QualifiedModel())
}

// ModelCount returns the number of models currently loaded into the router lookup.
func (r *Router) ModelCount() int {
	if r == nil || r.lookup == nil {
		return 0
	}
	return r.lookup.ModelCount()
}
