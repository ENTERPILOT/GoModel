package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// gatedListProvider holds its ListModels until release is closed, and closes
// started when the first call begins.
type gatedListProvider struct {
	registryMockProvider
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func newGatedListProvider(err error, modelIDs ...string) *gatedListProvider {
	return &gatedListProvider{
		modelsResponse: modelsList(modelIDs...),
		err:            err,
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (p *gatedListProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return p.registryMockProvider.ListModels(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func modelsList(modelIDs ...string) *core.ModelsResponse {
	resp := &core.ModelsResponse{Object: "list"}
	for _, id := range modelIDs {
		resp.Data = append(resp.Data, core.Model{ID: id, Object: "model", OwnedBy: "test"})
	}
	return resp
}

// keyedProviderFactory builds, for each credential, the provider registered
// under its first API key.
func keyedProviderFactory(byKey map[string]core.Provider) *ProviderFactory {
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
			return byKey[cfg.APIKeys[0]]
		},
	})
	return factory
}

// routeWatcher resolves model on registry until stopped and counts every
// lookup that found no provider.
type routeWatcher struct {
	misses atomic.Int64
	stop   chan struct{}
	done   chan struct{}
}

func watchRoute(registry *ModelRegistry, model string) *routeWatcher {
	w := &routeWatcher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			if registry.GetProvider(model) == nil {
				w.misses.Add(1)
			}
		}
	}()
	return w
}

func (w *routeWatcher) Stop() int64 {
	close(w.stop)
	<-w.done
	return w.misses.Load()
}

// TestCredentialsService_UpsertKeepsModelsRoutableWhileReplacementFetches is
// the regression test for replacing a provider's credentials: the models it
// served must stay routable, through the replacement, while the replacement's
// own /models call is still outstanding.
func TestCredentialsService_UpsertKeepsModelsRoutableWhileReplacementFetches(t *testing.T) {
	ctx := t.Context()
	first := &registryMockProvider{modelsResponse: modelsList("kept", "retired")}
	second := newGatedListProvider(nil, "kept", "added")
	registry := NewModelRegistry()
	svc, err := NewCredentialsService(ctx, keyedProviderFactory(map[string]core.Provider{"sk-one": first, "sk-two": second}),
		registry, newFakeCredentialStore(), nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)
	cred := ManagedProviderCredential{Name: "swap", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}
	require.NoError(t, svc.Upsert(ctx, cred))
	inFlight := registry.GetModel("swap/kept")
	require.NotNil(t, inFlight)
	require.Same(t, first, inFlight.Provider)

	watcher := watchRoute(registry, "swap/kept")
	cred.APIKeys = []string{"sk-two"}
	saved := make(chan error, 1)
	go func() { saved <- svc.Upsert(ctx, cred) }()
	<-second.started

	// The replacement is installed and its fetch is blocked: the inherited
	// inventory serves through it.
	assert.Same(t, second, registry.GetProvider("swap/kept"))
	assert.Same(t, second, registry.GetProvider("kept"))
	assert.True(t, registry.Supports("swap/retired"))
	assert.False(t, registry.Supports("swap/added"))
	// A request that resolved the previous provider keeps it.
	assert.Same(t, first, inFlight.Provider)

	close(second.release)
	require.NoError(t, <-saved)
	assert.Zero(t, watcher.Stop(), "lookups found no provider during the swap")

	// The replacement's inventory replaced the inherited one.
	assert.Same(t, second, registry.GetProvider("swap/kept"))
	assert.True(t, registry.Supports("swap/added"))
	assert.False(t, registry.Supports("swap/retired"))
	assert.Equal(t, 1, registry.ProviderCount())
}

// TestCredentialsService_UpsertKeepsInheritedModelsWhenReplacementFetchFails
// covers a replacement whose /models call fails, such as a key without the
// models permission or an upstream control-plane outage: the inherited
// inventory keeps serving through the replacement, and the failure is reported.
func TestCredentialsService_UpsertKeepsInheritedModelsWhenReplacementFetchFails(t *testing.T) {
	ctx := t.Context()
	first := &registryMockProvider{modelsResponse: modelsList("kept")}
	second := &registryMockProvider{err: errors.New("503 models unavailable")}
	other := &registryMockProvider{modelsResponse: modelsList("other-model")}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(other, "other", "test")
	svc, err := NewCredentialsService(ctx, keyedProviderFactory(map[string]core.Provider{"sk-one": first, "sk-two": second}),
		registry, newFakeCredentialStore(), nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)
	cred := ManagedProviderCredential{Name: "swap", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}
	require.NoError(t, svc.Upsert(ctx, cred))
	require.Same(t, first, registry.GetProvider("swap/kept"))

	cred.APIKeys = []string{"sk-two"}
	require.NoError(t, svc.Upsert(ctx, cred))

	assert.Same(t, second, registry.GetProvider("swap/kept"))
	var snapshot *ProviderRuntimeSnapshot
	for _, s := range registry.ProviderRuntimeSnapshots() {
		if s.Name == "swap" {
			snapshot = &s
		}
	}
	require.NotNil(t, snapshot)
	assert.Contains(t, snapshot.LastModelFetchError, "503 models unavailable")
	// A healthy alternative exists, so load balancing routes around it.
	assert.True(t, snapshot.InventoryStale)
	assert.False(t, registry.ModelAvailable("swap/kept"))
}

// TestCredentialsServiceRefreshKeepsModelsRoutableWhileReplacementFetches
// covers config sync: another instance edits the credential, and this
// instance's Refresh replaces the provider while its models are fetched in the
// background.
func TestCredentialsServiceRefreshKeepsModelsRoutableWhileReplacementFetches(t *testing.T) {
	ctx := t.Context()
	store := newFakeCredentialStore()
	first := &registryMockProvider{modelsResponse: modelsList("kept")}
	second := newGatedListProvider(nil, "kept")
	t.Cleanup(func() { close(second.release) })
	factory := keyedProviderFactory(map[string]core.Provider{"sk-one": first, "sk-two": second})
	cred := ManagedProviderCredential{Name: "swap", Type: "test", APIKeys: []string{"sk-one"}, Enabled: true}
	require.NoError(t, store.Upsert(ctx, cred))
	registry := NewModelRegistry()
	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(ctx))
	require.Same(t, first, registry.GetProvider("swap/kept"))

	cred.APIKeys = []string{"sk-two"}
	require.NoError(t, store.Upsert(ctx, cred))
	require.NoError(t, svc.Refresh(ctx))
	<-second.started

	assert.Same(t, second, registry.GetProvider("swap/kept"))
}

func TestModelRegistry_ReplaceProvider(t *testing.T) {
	t.Run("keeps the registration order for bare model IDs", func(t *testing.T) {
		registry := NewModelRegistry()
		first := &registryMockProvider{modelsResponse: modelsList("shared")}
		second := &registryMockProvider{modelsResponse: modelsList("shared")}
		registry.RegisterProviderWithNameAndType(first, "first", "test")
		registry.RegisterProviderWithNameAndType(second, "second", "test")
		require.NoError(t, registry.Initialize(t.Context()))

		replacement := &registryMockProvider{modelsResponse: modelsList("shared")}
		registry.replaceProvider(replacement, "first", "test", providerSettings{})

		assert.Same(t, replacement, registry.GetProvider("shared"))
		assert.Equal(t, 2, registry.ProviderCount())
	})

	t.Run("applies the new allowlist and filter to the inherited inventory", func(t *testing.T) {
		registry := NewModelRegistry()
		registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
		previous := &registryMockProvider{modelsResponse: modelsList("a", "b", "c")}
		registry.RegisterProviderWithNameAndType(previous, "swap", "test")
		require.NoError(t, registry.Initialize(t.Context()))

		replacement := &registryMockProvider{}
		registry.replaceProvider(replacement, "swap", "test", providerSettings{
			configuredModels: []string{"a", "b", "d"},
			modelFilter:      config.ModelFilter{Exclude: []string{"b"}},
		})

		assert.Same(t, replacement, registry.GetProvider("swap/a"))
		assert.False(t, registry.Supports("swap/b"), "excluded by the filter")
		assert.False(t, registry.Supports("swap/c"), "not on the allowlist")
		assert.Same(t, replacement, registry.GetProvider("swap/d"))
	})

	t.Run("applies the new metadata overrides to the inherited inventory", func(t *testing.T) {
		registry := NewModelRegistry()
		previous := &registryMockProvider{modelsResponse: modelsList("a")}
		registry.RegisterProviderWithNameAndType(previous, "swap", "test")
		require.NoError(t, registry.Initialize(t.Context()))

		registry.replaceProvider(&registryMockProvider{}, "swap", "test", providerSettings{
			metadataOverrides: map[string]*core.ModelMetadata{"a": {DisplayName: "Model A"}},
		})

		info := registry.GetModel("swap/a")
		require.NotNil(t, info)
		require.NotNil(t, info.Model.Metadata)
		assert.Equal(t, "Model A", info.Model.Metadata.DisplayName)
	})

	t.Run("a provider of another type inherits nothing", func(t *testing.T) {
		registry := NewModelRegistry()
		previous := &registryMockProvider{modelsResponse: modelsList("a")}
		registry.RegisterProviderWithNameAndType(previous, "swap", "test")
		require.NoError(t, registry.Initialize(t.Context()))

		registry.replaceProvider(&registryMockProvider{}, "swap", "other", providerSettings{})

		assert.False(t, registry.Supports("swap/a"))
		assert.Equal(t, "other", registry.GetProviderTypeForName("swap"))
	})

	t.Run("a fetch the previous provider started keeps the inherited inventory", func(t *testing.T) {
		registry := NewModelRegistry()
		previous := newGatedListProvider(nil, "kept", "from-old-fetch")
		registry.RegisterProviderWithNameAndType(previous, "swap", "test")
		close(previous.release)
		require.NoError(t, registry.Initialize(t.Context()))

		// A sweep that starts on the previous provider and is still running
		// when the provider is replaced.
		stalled := newGatedListProvider(nil, "stale-sweep-model")
		registry.replaceProvider(stalled, "swap", "test", providerSettings{})
		sweep := make(chan error, 1)
		go func() { sweep <- registry.Refresh(t.Context()) }()
		<-stalled.started

		replacement := &registryMockProvider{}
		registry.replaceProvider(replacement, "swap", "test", providerSettings{})
		close(stalled.release)
		require.NoError(t, <-sweep)

		assert.False(t, registry.Supports("swap/stale-sweep-model"), "the superseded result is discarded")
		assert.Same(t, replacement, registry.GetProvider("swap/kept"))
		assert.Same(t, replacement, registry.GetProvider("swap/from-old-fetch"))
	})
}
