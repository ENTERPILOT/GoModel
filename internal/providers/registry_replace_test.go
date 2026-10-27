package providers

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type replacingRegistryProvider struct {
	*registryMockProvider
	blockList   bool
	listStarted chan struct{}
	listRelease chan struct{}
	listErr     error
	chatStarted chan struct{}
	chatRelease chan struct{}
}

func waitForRegistryReplacementSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForRegistryReplacementError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func (p *replacingRegistryProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if !p.blockList {
		return p.registryMockProvider.ListModels(ctx)
	}
	close(p.listStarted)
	select {
	case <-p.listRelease:
		return p.modelsResponse, p.listErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *replacingRegistryProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	close(p.chatStarted)
	select {
	case <-p.chatRelease:
		return p.chatResponse, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestModelRegistry_ReplaceProviderWithConfigPreservesEligibleInventory(t *testing.T) {
	registry := NewModelRegistry()
	oldProvider := &registryMockProvider{modelsResponse: &core.ModelsResponse{Data: []core.Model{
		{ID: "shared", Object: "model", OwnedBy: "old"},
		{ID: "removed", Object: "model", OwnedBy: "old"},
	}}}
	otherProvider := &registryMockProvider{modelsResponse: &core.ModelsResponse{Data: []core.Model{
		{ID: "other-model", Object: "model", OwnedBy: "other"},
	}}}
	registry.RegisterProviderWithNameAndType(oldProvider, "managed", "test")
	registry.RegisterProviderWithNameAndType(otherProvider, "other", "test")
	require.NoError(t, registry.Initialize(t.Context()))

	registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
	newProvider := &registryMockProvider{modelsResponse: &core.ModelsResponse{Data: []core.Model{
		{ID: "new-model", Object: "model", OwnedBy: "new"},
	}}}
	contextWindow := 8192
	registry.ReplaceProviderWithConfig(newProvider, ProviderConfig{
		Name:   "managed",
		Type:   "test",
		Models: []string{"shared", "added"},
		ModelFilter: config.ModelFilter{
			Include: []string{"shared"},
		},
		ModelMetadataOverrides: map[string]*core.ModelMetadata{
			"shared": {DisplayName: "Replacement", ContextWindow: &contextWindow},
		},
	})

	assert.Same(t, newProvider, registry.GetProvider("managed/shared"))
	assert.True(t, registry.Supports("managed/shared"))
	assert.False(t, registry.Supports("managed/removed"))
	assert.False(t, registry.Supports("managed/added"))
	assert.Same(t, otherProvider, registry.GetProvider("other/other-model"))
	model := registry.GetModel("managed/shared")
	require.NotNil(t, model)
	require.NotNil(t, model.Model.Metadata)
	assert.Equal(t, "Replacement", model.Model.Metadata.DisplayName)
	require.NotNil(t, model.Model.Metadata.ContextWindow)
	assert.Equal(t, contextWindow, *model.Model.Metadata.ContextWindow)
}

func TestModelRegistry_ReplacementKeepsInventoryAcrossStaleFetch(t *testing.T) {
	for _, test := range []struct {
		name     string
		fetchErr error
	}{
		{name: "success"},
		{name: "failure", fetchErr: errors.New("stale model fetch failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewModelRegistry()
			oldProvider := &replacingRegistryProvider{
				registryMockProvider: &registryMockProvider{
					modelsResponse: &core.ModelsResponse{Data: []core.Model{{ID: "kept", Object: "model"}}},
					chatResponse:   &core.ChatResponse{},
				},
				listStarted: make(chan struct{}),
				listRelease: make(chan struct{}),
				listErr:     test.fetchErr,
				chatStarted: make(chan struct{}),
				chatRelease: make(chan struct{}),
			}
			var releaseListOnce, releaseChatOnce sync.Once
			releaseList := func() { releaseListOnce.Do(func() { close(oldProvider.listRelease) }) }
			releaseChat := func() { releaseChatOnce.Do(func() { close(oldProvider.chatRelease) }) }
			t.Cleanup(releaseList)
			t.Cleanup(releaseChat)
			otherProvider := &registryMockProvider{modelsResponse: &core.ModelsResponse{Data: []core.Model{{ID: "other-model", Object: "model"}}}}
			registry.RegisterProviderWithNameAndType(oldProvider, "managed", "test")
			registry.RegisterProviderWithNameAndType(otherProvider, "other", "test")
			require.NoError(t, registry.Initialize(t.Context()))
			before := registry.ProviderRuntimeSnapshots()[0]

			oldProvider.modelsResponse = &core.ModelsResponse{Data: []core.Model{{ID: "stale", Object: "model"}}}
			oldProvider.blockList = true
			refreshDone := make(chan error, 1)
			go func() { refreshDone <- registry.Initialize(t.Context()) }()
			waitForRegistryReplacementSignal(t, oldProvider.listStarted, "blocked old-provider model fetch")

			inflightDone := make(chan error, 1)
			go func() {
				_, err := oldProvider.ChatCompletion(t.Context(), &core.ChatRequest{})
				inflightDone <- err
			}()
			waitForRegistryReplacementSignal(t, oldProvider.chatStarted, "in-flight old-provider request")

			newProvider := &registryMockProvider{modelsResponse: &core.ModelsResponse{Data: []core.Model{{ID: "new-model", Object: "model"}}}}
			registry.ReplaceProviderWithConfig(newProvider, ProviderConfig{Name: "managed", Type: "test"})
			currentProvider := core.Provider(newProvider)
			assert.True(t, registry.Supports("managed/kept"))
			assert.Same(t, newProvider, registry.GetProvider("managed/kept"))
			assert.Same(t, otherProvider, registry.GetProvider("other/other-model"))

			var missing atomic.Bool
			var lookupCount atomic.Uint64
			lookupStarted := make(chan struct{})
			stopLookups := make(chan struct{})
			lookupsDone := make(chan struct{})
			go func() {
				defer close(lookupsDone)
				for {
					provider := registry.GetProvider("managed/kept")
					if lookupCount.Add(1) == 1 {
						close(lookupStarted)
					}
					if provider == nil {
						missing.Store(true)
						return
					}
					select {
					case <-stopLookups:
						return
					default:
					}
				}
			}()
			waitForRegistryReplacementSignal(t, lookupStarted, "first concurrent provider lookup")
			assert.Positive(t, lookupCount.Load())
			for range 25 {
				currentProvider = &registryMockProvider{}
				registry.ReplaceProviderWithConfig(currentProvider, ProviderConfig{Name: "managed", Type: "test"})
				runtime.Gosched()
			}
			close(stopLookups)
			waitForRegistryReplacementSignal(t, lookupsDone, "concurrent provider lookup loop")
			assert.Positive(t, lookupCount.Load())
			assert.False(t, missing.Load(), "lookups must never observe an unregistered gap")

			releaseList()
			releaseChat()
			require.NoError(t, waitForRegistryReplacementError(t, inflightDone, "in-flight old-provider request"), "the request already using the old adapter completes")
			refreshErr := waitForRegistryReplacementError(t, refreshDone, "registry refresh")
			require.NoError(t, refreshErr, "the unaffected provider keeps the sweep healthy")
			assert.True(t, registry.Supports("managed/kept"))
			assert.Same(t, currentProvider, registry.GetProvider("managed/kept"))
			assert.Nil(t, registry.GetProvider("managed/stale"))
			assert.Equal(t, before.LastModelFetchAt, registry.ProviderRuntimeSnapshots()[0].LastModelFetchAt)
			assert.Empty(t, registry.ProviderRuntimeSnapshots()[0].LastModelFetchError)
		})
	}
}

var _ core.Provider = (*replacingRegistryProvider)(nil)
