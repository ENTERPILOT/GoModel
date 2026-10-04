package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingVault serves ${vault:...} references and records every lookup.
func countingVault(t *testing.T, values map[string]string) (*config.Secrets, *[]string) {
	t.Helper()
	var lookups []string
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		lookups = append(lookups, reference)
		value, ok := values[reference]
		if !ok {
			return "", errors.New("not found")
		}
		return value, nil
	})))
	return secrets, &lookups
}

func TestResolveProvidersResolvesSecretReferences(t *testing.T) {
	global := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	tests := []struct {
		name        string
		env         map[string]string
		raw         map[string]config.RawProviderConfig
		wantKeys    map[string][]string
		wantBaseURL map[string]string
		wantLookups []string
		wantErr     string
	}{
		{
			name:        "yaml references",
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:openai}", BaseURL: "${vault:url}/v1/$${literal}"}},
			wantKeys:    map[string][]string{"openai": {"sk-openai"}},
			wantBaseURL: map[string]string{"openai": "https://eu.example.com/v1/${literal}"},
			wantLookups: []string{"openai", "url"},
		},
		{
			name:        "env references",
			env:         map[string]string{"OPENAI_API_KEY": "${vault:openai}", "OPENAI_API_KEY_2": "${vault:openai2}", "ANTHROPIC_BASE_URL": "${vault:url}", "ANTHROPIC_API_KEY": "sk-ant"},
			wantKeys:    map[string][]string{"openai": {"sk-openai", "sk-openai2"}, "anthropic": {"sk-ant"}},
			wantBaseURL: map[string]string{"openai": "https://api.openai.com/v1", "anthropic": "https://eu.example.com"},
		},
		{
			name:        "env replaces a yaml reference, which is never looked up",
			env:         map[string]string{"OPENAI_API_KEY": "sk-env"},
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:missing}"}},
			wantKeys:    map[string][]string{"openai": {"sk-env"}},
			wantLookups: []string{},
		},
		{
			name:        "yaml base_url reference is not replaced by the default",
			env:         map[string]string{"OPENAI_API_KEY": "sk-env"},
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", BaseURL: "${vault:url}"}},
			wantKeys:    map[string][]string{"openai": {"sk-env"}},
			wantBaseURL: map[string]string{"openai": "https://eu.example.com"},
		},
		{
			name:        "an ignored env var is never looked up",
			env:         map[string]string{"OPENAI_API_KEY": "${vault:missing}"},
			raw:         map[string]config.RawProviderConfig{"alpha": {Type: "openai", APIKey: "${vault:openai}", BaseURL: "https://alpha.example.com"}},
			wantKeys:    map[string][]string{"alpha": {"sk-openai"}},
			wantLookups: []string{"openai"},
		},
		{
			name:    "an unresolved yaml reference is an error",
			raw:     map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:missing}"}},
			wantErr: "providers.openai.api_key: secret reference ${vault:...}: not found",
		},
		{
			name:    "an unresolved env reference is an error",
			env:     map[string]string{"OPENAI_API_KEY": "${vault:missing}"},
			wantErr: "providers.openai.api_key: secret reference ${vault:...}: not found",
		},
		{
			name:        "a provider dropped for lack of credentials is never looked up",
			env:         map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", ProxyURL: "http://u:${vault:missing}@proxy:3128"}},
			wantKeys:    map[string][]string{"anthropic": {"sk-ant"}},
			wantLookups: []string{},
		},
		{
			name:        "a resolved key containing ${ is kept",
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:weird}", APIKeys: []string{"$${escaped}"}}},
			wantKeys:    map[string][]string{"openai": {"sk-${NOT_A_PLACEHOLDER}", "${escaped}"}},
			wantLookups: []string{"weird"},
		},
		{
			name:        "a key mixing a reference with a legacy placeholder is dropped unresolved",
			env:         map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			raw:         map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:openai}-${GOMODEL_TEST_UNSET}"}},
			wantKeys:    map[string][]string{"anthropic": {"sk-ant"}},
			wantLookups: []string{},
		},
		{
			name:     "legacy placeholders keep their historical treatment",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${GOMODEL_TEST_UNSET}"}},
			wantKeys: map[string][]string{"anthropic": {"sk-ant"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			secrets, lookups := countingVault(t, map[string]string{
				"openai": "sk-openai", "openai2": "sk-openai2", "url": "https://eu.example.com",
				"weird": "sk-${NOT_A_PLACEHOLDER}",
			})
			raw := tt.raw
			if raw == nil {
				raw = map[string]config.RawProviderConfig{}
			}

			got, _, err := resolveProviders(t.Context(), secrets, raw, global, testDiscoveryConfigs)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			gotKeys := make(map[string][]string, len(got))
			for name, cfg := range got {
				gotKeys[name] = cfg.APIKeys
			}
			assert.Equal(t, tt.wantKeys, gotKeys)
			for name, want := range tt.wantBaseURL {
				assert.Equal(t, want, got[name].BaseURL, name)
			}
			if tt.wantLookups != nil {
				assert.ElementsMatch(t, tt.wantLookups, *lookups)
			}
		})
	}
}

func TestResolveProvidersKeepsLoadedReferences(t *testing.T) {
	secrets, _ := countingVault(t, map[string]string{"a": "sk-a", "b": "sk-b"})
	raw := map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}", APIKeys: []string{"${vault:b}"}}}

	got, _, err := resolveProviders(t.Context(), secrets, raw, config.ResilienceConfig{}, testDiscoveryConfigs)
	require.NoError(t, err)
	assert.Equal(t, []string{"sk-a", "sk-b"}, got["openai"].APIKeys)
	assert.Equal(t, "${vault:a}", raw["openai"].APIKey)
	assert.Equal(t, []string{"${vault:b}"}, raw["openai"].APIKeys)
}

func TestInit_StopsOnUnresolvedProviderSecret(t *testing.T) {
	built := false
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(ProviderConfig, ProviderOptions) core.Provider {
			built = true
			return &initTestProvider{}
		},
	})
	t.Setenv("TEST_API_KEY", "${vault:prod/test}")

	_, err := Init(t.Context(), &config.LoadResult{
		Config: &config.Config{Cache: config.CacheConfig{Model: config.ModelCacheConfig{
			RefreshInterval: 1,
			Local:           &config.LocalCacheConfig{CacheDir: t.TempDir()},
		}}},
		RawProviders: map[string]config.RawProviderConfig{},
		Secrets:      config.NewSecrets(),
	}, factory)
	require.ErrorContains(t, err, "providers.test.api_key: secret reference ${vault:...}: unknown secret scheme")
	assert.False(t, built, "a provider must not be built from an unresolved reference")
}
