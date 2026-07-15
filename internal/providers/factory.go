// Package providers provides a factory for creating provider instances.
package providers

import (
	"fmt"
	"maps"
	"sort"
	"sync"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// ProviderOptions bundles runtime settings passed from the factory to provider constructors.
type ProviderOptions struct {
	Hooks      llmclient.Hooks
	Models     []string
	Resilience config.ResilienceConfig
	// Keys carries every API key configured for this provider instance. It is
	// nil for keyless providers and for constructors invoked outside the
	// factory; use the Keyring method rather than reading it directly.
	Keys *Keyring
}

// Keyring returns the key source a provider should authenticate with, falling
// back to a single-key ring over apiKey when the factory supplied none. Every
// provider constructor takes an API key and ProviderOptions, so this one call
// gives a provider rotation support without changing its signature, and keeps
// constructors invoked outside the factory (tests, the NewWithHTTPClient
// variants) working unchanged.
func (o ProviderOptions) Keyring(apiKey string) *Keyring {
	if o.Keys != nil {
		return o.Keys
	}
	return NewKeyring(apiKey)
}

// ProviderConstructor is the constructor signature for providers.
type ProviderConstructor func(cfg ProviderConfig, opts ProviderOptions) core.Provider

// DiscoveryConfig describes how a provider participates in config resolution.
// Env var names are derived by convention from Registration.Type.
type DiscoveryConfig struct {
	DefaultBaseURL     string
	RequireBaseURL     bool
	AllowAPIKeyless    bool
	SupportsAPIVersion bool
	NameSeparator      string
}

// Registration contains metadata for registering a provider with the factory.
type Registration struct {
	Type                        string
	New                         ProviderConstructor
	PassthroughSemanticEnricher core.PassthroughSemanticEnricher
	Discovery                   DiscoveryConfig
}

// ProviderFactory manages provider registration and creation.
type ProviderFactory struct {
	mu                   sync.RWMutex
	builders             map[string]ProviderConstructor
	discoveryConfigs     map[string]DiscoveryConfig
	passthroughEnrichers map[string]core.PassthroughSemanticEnricher
	hooks                llmclient.Hooks
}

// NewProviderFactory creates a new provider factory instance.
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		builders:             make(map[string]ProviderConstructor),
		discoveryConfigs:     make(map[string]DiscoveryConfig),
		passthroughEnrichers: make(map[string]core.PassthroughSemanticEnricher),
	}
}

// SetHooks configures observability hooks for all providers created by this factory.
func (f *ProviderFactory) SetHooks(hooks llmclient.Hooks) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = hooks
}

// AddHooks composes additional hooks with any already configured, affecting
// providers created after the call.
func (f *ProviderFactory) AddHooks(hooks llmclient.Hooks) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = llmclient.JoinHooks(f.hooks, hooks)
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
	f.discoveryConfigs[reg.Type] = reg.Discovery
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

	// One Keyring per provider instance: every client this provider builds
	// shares the rotation, so keys are used evenly across all its endpoints.
	opts := ProviderOptions{
		Hooks:      hooks,
		Models:     cfg.Models,
		Resilience: cfg.Resilience,
		Keys:       NewKeyring(cfg.APIKeys...),
	}

	return builder(cfg, opts), nil
}

// discoveryConfigsSnapshot returns provider discovery metadata keyed by provider type.
func (f *ProviderFactory) discoveryConfigsSnapshot() map[string]DiscoveryConfig {
	f.mu.RLock()
	defer f.mu.RUnlock()

	snapshot := make(map[string]DiscoveryConfig, len(f.discoveryConfigs))
	maps.Copy(snapshot, f.discoveryConfigs)
	return snapshot
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
