package providers

import (
	"context"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ListModels returns all models from the lookup.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if err := r.checkReady(); err != nil {
		return nil, registryUnavailableError(err)
	}
	var models []core.Model
	if r.caps.unqualifiedPublicModels != nil && r.unqualifiedModelIDs {
		models = r.caps.unqualifiedPublicModels.ListUnqualifiedPublicModels()
	} else if r.caps.publicModels != nil {
		models = r.caps.publicModels.ListPublicModels()
	} else {
		models = r.lookup.ListModels()
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   models,
	}, nil
}

// GetProviderType returns the provider type string for the given model.
// Returns empty string if the model is not found.
func (r *Router) GetProviderType(model string) string {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return ""
	}
	return r.lookup.GetProviderType(selector.QualifiedModel())
}

// GetProviderName returns the concrete configured provider instance name for
// the given model selector. Returns empty string when unavailable.
func (r *Router) GetProviderName(model string) string {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return ""
	}
	if !r.lookup.Supports(selector.QualifiedModel()) {
		return ""
	}
	if selector.Provider != "" {
		return selector.Provider
	}
	return r.lookup.GetProviderName(selector.QualifiedModel())
}

// GetProviderNameForType returns the concrete configured provider instance name
// chosen for a provider-typed route.
func (r *Router) GetProviderNameForType(providerType string) string {
	return r.lookup.GetProviderNameForType(providerType)
}

// GetProviderTypeForName returns the provider type for a concrete configured
// provider instance name.
func (r *Router) GetProviderTypeForName(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	return r.lookup.GetProviderTypeForName(providerName)
}

func (r *Router) providerByType(providerType string) core.Provider {
	models := r.lookup.ListModels()
	for _, model := range models {
		if r.lookup.GetProviderType(model.ID) != providerType {
			continue
		}
		p := r.lookup.GetProvider(model.ID)
		if p != nil {
			return p
		}
	}
	return nil
}

func (r *Router) providerByTypeRegistry(providerType string) core.Provider {
	if r.caps.typeRegistry != nil {
		if provider := r.caps.typeRegistry.ProviderByType(providerType); provider != nil {
			return provider
		}
	}
	return r.providerByType(providerType)
}

func (r *Router) providerByNameRegistry(providerName string) core.Provider {
	if r.caps.nameRegistry != nil {
		if provider := r.caps.nameRegistry.ProviderByName(providerName); provider != nil {
			return provider
		}
	}
	return r.providerByName(providerName)
}

// providerByName scans the catalog for a trimmed provider instance name.
func (r *Router) providerByName(providerName string) core.Provider {
	if providerName == "" || r.caps.modelsWithProvider == nil {
		return nil
	}
	for _, entry := range r.caps.modelsWithProvider.ListModelsWithProvider() {
		if entry.ProviderName != providerName || entry.Model.ID == "" {
			continue
		}
		if provider := r.lookup.GetProvider(core.ModelSelector{Provider: providerName, Model: entry.Model.ID}.QualifiedModel()); provider != nil {
			return provider
		}
	}
	return nil
}

func (r *Router) providerTypes() []string {
	if r.caps.typeLister != nil {
		return r.caps.typeLister.ProviderTypes()
	}

	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, model := range r.lookup.ListModels() {
		providerType := r.lookup.GetProviderType(model.ID)
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		result = append(result, providerType)
	}
	sort.Strings(result)
	return result
}

// providerTypesSupporting returns the registered provider types whose backing
// provider satisfies the given capability check. The inventory is independent of
// the public model catalog whenever the underlying lookup can expose provider
// types directly.
func (r *Router) providerTypesSupporting(supports func(core.Provider) bool) []string {
	providerTypes := r.providerTypes()
	result := make([]string, 0, len(providerTypes))
	for _, providerType := range providerTypes {
		provider := r.providerByTypeRegistry(providerType)
		if provider != nil && supports(provider) {
			result = append(result, providerType)
		}
	}
	return result
}

// NativeFileProviderTypes returns the registered provider types that support
// native file operations.
func (r *Router) NativeFileProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeFileProvider)
		return ok
	})
}

// NativeBatchProviderTypes returns the registered provider types that support
// native batch operations.
func (r *Router) NativeBatchProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeBatchProvider)
		return ok
	})
}

// NativeResponseProviderTypes returns the registered provider types that
// support native Responses lifecycle operations.
func (r *Router) NativeResponseProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeResponseLifecycleProvider)
		return ok
	})
}
