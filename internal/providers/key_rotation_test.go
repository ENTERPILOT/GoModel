package providers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotationVault is a mutable ${vault:...} backend.
type rotationVault struct {
	mu     sync.Mutex
	values map[string]string
}

func (v *rotationVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *rotationVault) ResolveSecret(_ context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

type rotationFixture struct {
	secrets  *config.Secrets
	vault    *rotationVault
	rotation *keyRotation
	keyrings map[string]*Keyring
}

// newRotationFixture resolves raw and environ the way a generation does
// (config walk, provider env vars, provider resolution) and keeps one keyring
// per provider.
func newRotationFixture(t *testing.T, values map[string]string, raw map[string]config.RawProviderConfig, environ []string) *rotationFixture {
	t.Helper()
	vault := &rotationVault{values: values}
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	result := &config.LoadResult{Config: &config.Config{}, RawProviders: raw, Secrets: secrets}
	require.NoError(t, result.ResolveSecrets(t.Context()))
	resolvedEnv, err := resolveProviderEnvSecrets(t.Context(), secrets, environ, testDiscoveryConfigs)
	require.NoError(t, err)
	providerMap, _ := resolveProviders(result.RawProviders, config.ResilienceConfig{}, testDiscoveryConfigs, resolvedEnv)
	keyrings := make(map[string]*Keyring, len(providerMap))
	for name, p := range providerMap {
		keyrings[name] = NewKeyringWithSessionStickiness(p.SessionStickyKeys, p.APIKeys...)
	}
	return &rotationFixture{
		secrets:  secrets,
		vault:    vault,
		rotation: newKeyRotation(result.RawProviders, resolvedEnv, testDiscoveryConfigs, config.ResilienceConfig{}, providerMap, keyrings),
		keyrings: keyrings,
	}
}

func (f *rotationFixture) plan(t *testing.T) (*config.SecretRecheck, *KeyRotation) {
	t.Helper()
	recheck, err := f.secrets.Recheck(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, recheck.Fields())
	return recheck, f.rotation.plan(recheck)
}

func ringKeys(ring *Keyring) []string {
	return append([]string(nil), ring.load()...)
}

func TestKeyRotationSwapsProviderKeys(t *testing.T) {
	tests := []struct {
		name     string
		raw      map[string]config.RawProviderConfig
		environ  []string
		rotate   string
		provider string
		want     []string
	}{
		{
			name:     "yaml api_key",
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}", APIKeys: []string{"literal"}}},
			rotate:   "a",
			provider: "openai",
			want:     []string{"a-new", "literal"},
		},
		{
			name:     "yaml api_keys entry",
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKeys: []string{"literal", "${vault:a}"}}},
			rotate:   "a",
			provider: "openai",
			want:     []string{"literal", "a-new"},
		},
		{
			name:     "numbered env var",
			raw:      map[string]config.RawProviderConfig{},
			environ:  []string{"OPENAI_API_KEY=literal", "OPENAI_API_KEY_2=${vault:a}"},
			rotate:   "a",
			provider: "openai",
			want:     []string{"literal", "a-new"},
		},
		{
			name:     "suffixed env var maps to its provider",
			raw:      map[string]config.RawProviderConfig{},
			environ:  []string{"OPENAI_API_KEY=other", "OPENAI_EU_API_KEY=${vault:a}"},
			rotate:   "a",
			provider: "openai-eu",
			want:     []string{"a-new"},
		},
		{
			name:     "bare env var overlays the one config provider of its type",
			raw:      map[string]config.RawProviderConfig{"primary": {Type: "openai", BaseURL: "https://example.test/v1"}},
			environ:  []string{"OPENAI_API_KEY=${vault:a}"},
			rotate:   "a",
			provider: "primary",
			want:     []string{"a-new"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRotationFixture(t, map[string]string{"a": "a-old"}, tt.raw, tt.environ)
			f.vault.set(tt.rotate, "a-new")

			recheck, plan := f.plan(t)
			require.NotNil(t, plan)
			assert.Equal(t, []string{tt.provider}, plan.Providers())
			assert.NotContains(t, ringKeys(f.keyrings[tt.provider]), "a-new", "planning must not swap")

			plan.Apply()
			recheck.Commit()
			assert.Equal(t, tt.want, ringKeys(f.keyrings[tt.provider]))

			// A second rotation diffs against the swapped state.
			f.vault.set(tt.rotate, "a-newer")
			recheck, plan = f.plan(t)
			require.NotNil(t, plan)
			plan.Apply()
			recheck.Commit()
			assert.Contains(t, ringKeys(f.keyrings[tt.provider]), "a-newer")
		})
	}
}

func TestKeyRotationNeedsReload(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]config.RawProviderConfig
		environ []string
		value   string
	}{
		{
			name:  "yaml base_url",
			raw:   map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "k", BaseURL: "${vault:a}"}},
			value: "https://other.test/v1",
		},
		{
			name:    "env base_url",
			raw:     map[string]config.RawProviderConfig{},
			environ: []string{"OPENAI_API_KEY=k", "OPENAI_BASE_URL=${vault:a}"},
			value:   "https://other.test/v1",
		},
		{
			name:  "yaml proxy_url composite",
			raw:   map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "k", ProxyURL: "http://u:${vault:a}@proxy:3128"}},
			value: "rotated",
		},
		{
			name:  "provider loses its last key",
			raw:   map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}"}},
			value: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRotationFixture(t, map[string]string{"a": "https://a.test/v1"}, tt.raw, tt.environ)
			f.vault.set("a", tt.value)
			_, plan := f.plan(t)
			assert.Nil(t, plan)
		})
	}
}

func TestKeyRotationShadowedKeyChangesNothing(t *testing.T) {
	// An env key replaces the YAML key set, so rotating the YAML key has no
	// effect on the provider.
	f := newRotationFixture(t, map[string]string{"a": "a-old"},
		map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}"}},
		[]string{"OPENAI_API_KEY=from-env"})
	f.vault.set("a", "a-new")
	_, plan := f.plan(t)
	require.NotNil(t, plan)
	assert.Empty(t, plan.Providers())
	plan.Apply()
	assert.Equal(t, []string{"from-env"}, ringKeys(f.keyrings["openai"]))
}

func TestInitResultPlanKeyRotation(t *testing.T) {
	var nilResult *InitResult
	assert.Nil(t, nilResult.PlanKeyRotation(nil))

	vault := &rotationVault{values: map[string]string{"test": "sk-1"}}
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	t.Setenv("TEST_API_KEY", "${vault:test}")
	t.Setenv("TEST_BASE_URL", "http://localhost:1")

	var keys *Keyring
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			keys = opts.Keys
			return &initTestProvider{}
		},
	})
	result, err := Init(t.Context(), &config.LoadResult{
		Config: &config.Config{Cache: config.CacheConfig{Model: config.ModelCacheConfig{
			RefreshInterval: 1,
			Local:           &config.LocalCacheConfig{CacheDir: t.TempDir()},
		}}},
		RawProviders: map[string]config.RawProviderConfig{},
		Secrets:      secrets,
	}, factory)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Close() })
	require.Equal(t, "sk-1", keys.Primary())

	vault.set("test", "sk-2")
	recheck, err := secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"TEST_API_KEY"}, recheck.Fields())
	plan := result.PlanKeyRotation(recheck)
	require.NotNil(t, plan)
	assert.Equal(t, []string{"test"}, plan.Providers())
	plan.Apply()
	assert.Equal(t, "sk-2", keys.Primary(), "the provider's own keyring is swapped")
}
