package providers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// countingListProvider counts its model listings.
type countingListProvider struct {
	registryMockProvider
	listings atomic.Int64
}

func (p *countingListProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	p.listings.Add(1)
	return p.registryMockProvider.ListModels(ctx)
}

// Saving or deleting one provider must not list the models of the others: a
// sweep over every provider makes each save as slow as the slowest upstream.
func TestCredentialsService_SaveAndDeleteFetchOnlyTheSavedProvider(t *testing.T) {
	ctx := t.Context()
	other := &countingListProvider{modelsResponse: modelsList("shared", "other-only")}
	saved := &countingListProvider{modelsResponse: modelsList("shared", "saved-only")}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(other, "other", "test")
	require.NoError(t, registry.Initialize(ctx))
	svc, err := NewCredentialsService(ctx, keyedProviderFactory(map[string]core.Provider{"sk": saved}),
		registry, newFakeCredentialStore(), nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)
	other.listings.Store(0)

	cred := ManagedProviderCredential{Name: "saved", Type: "test", APIKeys: []string{"sk"}, Enabled: true}
	require.NoError(t, svc.Upsert(ctx, cred))
	assert.EqualValues(t, 1, saved.listings.Load())
	assert.True(t, registry.Supports("saved/saved-only"))
	assert.True(t, registry.Supports("other/other-only"))

	// Disabling fetches nothing and drops the provider's models at once.
	cred.Enabled = false
	require.NoError(t, svc.Upsert(ctx, cred))
	assert.EqualValues(t, 1, saved.listings.Load())
	assert.False(t, registry.Supports("saved/saved-only"))

	cred.Enabled = true
	require.NoError(t, svc.Upsert(ctx, cred))
	require.NoError(t, svc.Delete(ctx, "saved"))
	assert.EqualValues(t, 2, saved.listings.Load())
	assert.False(t, registry.Supports("saved/saved-only"))
	// The shared model stays routable through the provider that remains.
	assert.Same(t, other, registry.GetProvider("shared"))

	assert.Zero(t, other.listings.Load(), "saves and the delete listed another provider's models")
}

// unprobeableProvider fails its availability probe and its model listing, as
// a Bedrock key that may invoke models but not list them does.
type unprobeableProvider struct {
	registryMockProvider
}

func (p *unprobeableProvider) CheckAvailability(context.Context) error {
	return errors.New("not authorized to list models")
}

// A provider that cannot be probed still serves the models it is configured
// with: a save must not skip them because the probe failed.
func TestCredentialsService_SaveLoadsConfiguredModelsWhenTheProbeFails(t *testing.T) {
	ctx := t.Context()
	saved := &unprobeableProvider{registryMockProvider{err: errors.New("not authorized to list models")}}
	registry := NewModelRegistry()
	svc, err := NewCredentialsService(ctx, keyedProviderFactory(map[string]core.Provider{"sk": saved}),
		registry, newFakeCredentialStore(), nil, config.ResilienceConfig{}, nil)
	require.NoError(t, err)

	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{
		Name: "saved", Type: "test", APIKeys: []string{"sk"}, Models: []string{"configured"}, Enabled: true,
	}))

	assert.True(t, registry.Supports("saved/configured"))
}
