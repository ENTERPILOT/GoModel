// Package providers provides a factory for creating provider instances.
package providers

import (
	"fmt"
	"sort"
	"sync"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

// ProviderOptions bundles runtime settings passed from the factory to provider constructors.
type ProviderOptions struct {
	Hooks      llmclient.Hooks
	Resilience config.ResilienceConfig
}

// ProviderConstructor is the constructor signature for providers.
type ProviderConstructor func(apiKey string, opts ProviderOptions) core.Provider

// Registration contains metadata for registering a provider with the factory.
type Registration struct {
	Type                        string
	New                         ProviderConstructor
	PassthroughSemanticEnricher core.PassthroughSemanticEnricher
}

// ProviderFactory manages provider registration and creation.
type ProviderFactory struct {
	mu                   sync.RWMutex
	builders             map[string]ProviderConstructor
	passthroughEnrichers map[string]core.PassthroughSemanticEnricher
	hooks                llmclient.Hooks
}

// NewProviderFactory creates a new provider factory instance.
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		builders:             make(map[string]ProviderConstructor),
		passthroughEnrichers: make(map[string]core.PassthroughSemanticEnricher),
	}
}

// SetHooks configures observability hooks for all providers created by this factory.
func (f *ProviderFactory) SetHooks(hooks llmclient.Hooks) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = hooks
}

// Add adds a provider constructor to the factory.
// Panics if reg.Type is empty or reg.New is nil — both are programming errors
// caught at startup, not runtime conditions.
func (f *ProviderFactory) Add(reg Registration) {
	if reg.Type == "" {
		panic("providers: Add called with empty Type")
	}
	if reg.New == nil {
		panic(fmt.Sprintf("providers: Add called with nil constructor for type %q", reg.Type))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builders[reg.Type] = reg.New
	if reg.PassthroughSemanticEnricher != nil {
		f.passthroughEnrichers[reg.Type] = reg.PassthroughSemanticEnricher
	} else {
		delete(f.passthroughEnrichers, reg.Type)
	}
}

// Create instantiates a provider based on its resolved configuration.
func (f *ProviderFactory) Create(cfg ProviderConfig) (core.Provider, error) {
	f.mu.RLock()
	builder, ok := f.builders[cfg.Type]
	hooks := f.hooks
	f.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}

	opts := ProviderOptions{
		Hooks:      hooks,
		Resilience: cfg.Resilience,
	}

	p := builder(cfg.APIKey, opts)

	if cfg.BaseURL != "" {
		if setter, ok := p.(interface{ SetBaseURL(string) }); ok {
			setter.SetBaseURL(cfg.BaseURL)
		}
	}
	if cfg.APIVersion != "" {
		if setter, ok := p.(interface{ SetAPIVersion(string) }); ok {
			setter.SetAPIVersion(cfg.APIVersion)
		}
	}

	return p, nil
}

// RegisteredTypes returns a list of all registered provider types.
func (f *ProviderFactory) RegisteredTypes() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	types := make([]string, 0, len(f.builders))
	for t := range f.builders {
		types = append(types, t)
	}
	return types
}

// PassthroughSemanticEnrichers returns registered passthrough semantic
// enrichers in deterministic provider-type order.
func (f *ProviderFactory) PassthroughSemanticEnrichers() []core.PassthroughSemanticEnricher {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.passthroughEnrichers) == 0 {
		return nil
	}

	types := make([]string, 0, len(f.passthroughEnrichers))
	for providerType := range f.passthroughEnrichers {
		types = append(types, providerType)
	}
	sort.Strings(types)

	enrichers := make([]core.PassthroughSemanticEnricher, 0, len(types))
	for _, providerType := range types {
		if enricher := f.passthroughEnrichers[providerType]; enricher != nil {
			enrichers = append(enrichers, enricher)
		}
	}
	if len(enrichers) == 0 {
		return nil
	}
	return enrichers
}
