package providers

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestProviderModelsModeOverridesGlobalMode(t *testing.T) {
	upstream := &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "listed-a", Object: "model"},
		{ID: "listed-b", Object: "model"},
	}}
	tests := []struct {
		name       string
		global     config.ConfiguredProviderModelsMode
		override   config.ConfiguredProviderModelsMode
		wantModels []string
		wantList   bool
	}{
		{name: "no override keeps global fallback", global: config.ConfiguredProviderModelsModeFallback, wantModels: []string{"listed-a", "listed-b"}, wantList: true},
		{name: "allowlist override under global fallback", global: config.ConfiguredProviderModelsModeFallback, override: config.ConfiguredProviderModelsModeAllowlist, wantModels: []string{"configured"}},
		{name: "merge override under global fallback", global: config.ConfiguredProviderModelsModeFallback, override: config.ConfiguredProviderModelsModeMerge, wantModels: []string{"configured", "listed-a", "listed-b"}, wantList: true},
		{name: "fallback override under global allowlist", global: config.ConfiguredProviderModelsModeAllowlist, override: config.ConfiguredProviderModelsModeFallback, wantModels: []string{"listed-a", "listed-b"}, wantList: true},
		{name: "override is case-insensitive", global: config.ConfiguredProviderModelsModeFallback, override: " AllowList ", wantModels: []string{"configured"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewModelRegistry()
			registry.SetConfiguredProviderModelsMode(tt.global)
			var listCount atomic.Int32
			mock := &countingRegistryMockProvider{
				listCount:            &listCount,
				registryMockProvider: &registryMockProvider{name: "router", modelsResponse: upstream},
			}
			registry.RegisterProviderWithNameAndType(mock, "router", "openrouter")
			registry.SetProviderConfiguredModels("router", []string{"configured"})
			registry.SetProviderModelsMode("router", tt.override)

			require.NoError(t, registry.Initialize(context.Background()))

			assert.Equal(t, tt.wantList, listCount.Load() > 0, "upstream /models called")
			assert.Equal(t, len(tt.wantModels), registry.ModelCount())
			for _, id := range tt.wantModels {
				assert.True(t, registry.Supports("router/"+id), "router/%s", id)
			}
		})
	}
}

func TestProviderModelsModeOnlyAffectsItsProvider(t *testing.T) {
	registry := NewModelRegistry()
	upstream := &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "listed", Object: "model"}}}
	for _, name := range []string{"strict", "loose"} {
		registry.RegisterProviderWithNameAndType(&registryMockProvider{name: name, modelsResponse: upstream}, name, "openai")
		registry.SetProviderConfiguredModels(name, []string{"configured"})
	}
	registry.SetProviderModelsMode("strict", config.ConfiguredProviderModelsModeAllowlist)

	require.NoError(t, registry.Initialize(context.Background()))

	assert.True(t, registry.Supports("strict/configured"))
	assert.False(t, registry.Supports("strict/listed"))
	assert.True(t, registry.Supports("loose/listed"))
	assert.False(t, registry.Supports("loose/configured"))
}

func TestApplyProviderEnvVars_ModelsMode(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or")
	t.Setenv("OPENROUTER_MODELS", "qwen/qwen3-vl-8b-instruct")
	t.Setenv("OPENROUTER_MODELS_MODE", "allowlist")

	got, _ := applyEnv(t, map[string]config.RawProviderConfig{
		"anthropic": {Type: "anthropic", APIKey: "sk-ant", ModelsMode: config.ConfiguredProviderModelsModeMerge},
	})

	require.Contains(t, got, "openrouter")
	assert.Equal(t, config.ConfiguredProviderModelsModeAllowlist, got["openrouter"].ModelsMode)
	assert.Equal(t, []string{"qwen/qwen3-vl-8b-instruct"}, config.ProviderModelIDs(got["openrouter"].Models))
	assert.Equal(t, config.ConfiguredProviderModelsModeMerge, got["anthropic"].ModelsMode)
}

func TestValidateProviderModelsModes(t *testing.T) {
	for _, mode := range []config.ConfiguredProviderModelsMode{"", "fallback", "allowlist", "merge", " Merge "} {
		require.NoError(t, validateProviderModelsModes(map[string]ProviderConfig{"p": {ModelsMode: mode}}), "mode %q", mode)
	}
	err := validateProviderModelsModes(map[string]ProviderConfig{"p": {ModelsMode: "only"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "providers.p.models_mode")
}

func TestValidateCredential_ModelsMode(t *testing.T) {
	schema := credentialSchema("openai", DiscoveryConfig{AllowAPIKeyless: true})
	field, ok := schema.Field(CredentialFieldModelsMode)
	require.True(t, ok)
	assert.True(t, field.Advanced)
	assert.Equal(t, []string{"fallback", "allowlist", "merge"}, field.Options)

	for _, mode := range []string{"", "fallback", "allowlist", "merge"} {
		require.NoError(t, validateCredential(ManagedProviderCredential{Name: "p", Type: "openai", ModelsMode: mode}, schema), "mode %q", mode)
	}
	err := validateCredential(ManagedProviderCredential{Name: "p", Type: "openai", ModelsMode: "only"}, schema)
	var fieldErr *CredentialFieldError
	require.ErrorAs(t, err, &fieldErr)
	assert.Equal(t, CredentialFieldModelsMode, fieldErr.Field)
}

func TestCredentialStoresRoundTripModelsMode(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{
			Name:       "openrouter",
			Type:       "openrouter",
			Models:     []string{"qwen/qwen3-vl-8b-instruct"},
			ModelsMode: "allowlist",
			Enabled:    true,
		}))
		got, err := store.Get(ctx, "openrouter")
		require.NoError(t, err)
		assert.Equal(t, "allowlist", got.ModelsMode)
		assert.Equal(t, config.ConfiguredProviderModelsModeAllowlist, got.toRawProviderConfig().ModelsMode)
	})
}
