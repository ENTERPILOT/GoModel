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

// newRotationFixture resolves raw and env the way a generation does (env
// overlay, then secret resolution, then normalization) and keeps one keyring
// per provider.
func newRotationFixture(t *testing.T, values map[string]string, raw map[string]config.RawProviderConfig, env map[string]string) *rotationFixture {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
	vault := &rotationVault{values: values}
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	sources, err := mergeProviderSources(t.Context(), secrets, raw, testDiscoveryConfigs)
	require.NoError(t, err)
	providerMap, _ := finishProviders(sources, config.ResilienceConfig{}, testDiscoveryConfigs)
	keyrings := make(map[string]*Keyring, len(providerMap))
	for name, p := range providerMap {
		keyrings[name] = NewKeyringWithSessionStickiness(p.SessionStickyKeys, p.APIKeys...)
	}
	return &rotationFixture{
		secrets:  secrets,
		vault:    vault,
		rotation: newKeyRotation(sources, testDiscoveryConfigs, config.ResilienceConfig{}, providerMap, keyrings),
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
		env      map[string]string
		rotate   string
		provider string
		fields   []string
		want     []string
	}{
		{
			name:     "yaml api_key",
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}", APIKeys: []string{"literal"}}},
			rotate:   "a",
			provider: "openai",
			fields:   []string{"providers.openai.api_key"},
			want:     []string{"a-new", "literal"},
		},
		{
			name:     "yaml api_keys entry",
			raw:      map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKeys: []string{"literal", "${vault:a}"}}},
			rotate:   "a",
			provider: "openai",
			fields:   []string{"providers.openai.api_keys[1]"},
			want:     []string{"literal", "a-new"},
		},
		{
			name:     "numbered env var",
			raw:      map[string]config.RawProviderConfig{},
			env:      map[string]string{"OPENAI_API_KEY": "literal", "OPENAI_API_KEY_2": "${vault:a}"},
			rotate:   "a",
			provider: "openai",
			fields:   []string{"OPENAI_API_KEY_2"},
			want:     []string{"literal", "a-new"},
		},
		{
			name:     "suffixed env var maps to its provider",
			raw:      map[string]config.RawProviderConfig{},
			env:      map[string]string{"OPENAI_API_KEY": "other", "OPENAI_EU_API_KEY": "${vault:a}"},
			rotate:   "a",
			provider: "openai-eu",
			fields:   []string{"OPENAI_EU_API_KEY"},
			want:     []string{"a-new"},
		},
		{
			name:     "bare env var overlays the one config provider of its type",
			raw:      map[string]config.RawProviderConfig{"primary": {Type: "openai", BaseURL: "https://example.test/v1"}},
			env:      map[string]string{"OPENAI_API_KEY": "${vault:a}"},
			rotate:   "a",
			provider: "primary",
			fields:   []string{"OPENAI_API_KEY"},
			want:     []string{"a-new"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRotationFixture(t, map[string]string{"a": "a-old"}, tt.raw, tt.env)
			f.vault.set(tt.rotate, "a-new")

			recheck, plan := f.plan(t)
			assert.Equal(t, tt.fields, recheck.Fields(), "env-supplied keys are recorded under their provider")
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
		name  string
		raw   map[string]config.RawProviderConfig
		env   map[string]string
		value string
	}{
		{
			name:  "yaml base_url",
			raw:   map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "k", BaseURL: "${vault:a}"}},
			value: "https://other.test/v1",
		},
		{
			name:  "env base_url",
			raw:   map[string]config.RawProviderConfig{},
			env:   map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "${vault:a}"},
			value: "https://other.test/v1",
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
			f := newRotationFixture(t, map[string]string{"a": "https://a.test/v1"}, tt.raw, tt.env)
			f.vault.set("a", tt.value)
			_, plan := f.plan(t)
			assert.Nil(t, plan)
		})
	}
}

func TestKeyRotationShadowedYAMLKeyIsNotTracked(t *testing.T) {
	// An env key replaces the YAML key set, so the YAML reference is never
	// resolved, never recorded, and rotating it changes nothing.
	f := newRotationFixture(t, map[string]string{"a": "a-old"},
		map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}"}},
		map[string]string{"OPENAI_API_KEY": "from-env"})
	f.vault.set("a", "a-new")
	recheck, err := f.secrets.Recheck(t.Context())
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields())
}

func TestKeyRotationWithoutEffectiveChange(t *testing.T) {
	// The rotated value trims to a key the provider already has, so its key
	// set stays the same: nothing to swap, but the change is still applied.
	f := newRotationFixture(t, map[string]string{"a": "k1"},
		map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "k1", APIKeys: []string{"${vault:a}"}}},
		nil)
	f.vault.set("a", "k1 ")
	_, plan := f.plan(t)
	require.NotNil(t, plan)
	assert.Empty(t, plan.Providers())
	plan.Apply()
	assert.Equal(t, []string{"k1"}, ringKeys(f.keyrings["openai"]))
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

func TestProviderSecretFieldIsTheSourceTheOperatorWrote(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "literal")
	t.Setenv("OPENAI_API_KEY_2", "${vault:a}")
	var fields []string
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(ctx context.Context, _ string) (string, error) {
		field, _ := config.SecretFieldFromContext(ctx)
		fields = append(fields, field)
		return "sk", nil
	})))
	_, err := mergeProviderSources(t.Context(), secrets, nil, testDiscoveryConfigs)
	require.NoError(t, err)
	assert.Equal(t, []string{"OPENAI_API_KEY_2"}, fields, "a resolver sees the variable the operator set")
}

func TestKeyRotationKeepsKeyOrder(t *testing.T) {
	f := newRotationFixture(t, map[string]string{"b": "b-old"},
		map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "k0", APIKeys: []string{"k1", "${vault:b}", "k3"}}},
		nil)
	f.vault.set("b", "b-new")
	recheck, plan := f.plan(t)
	assert.Equal(t, []string{"providers.openai.api_keys[1]"}, recheck.Fields(), "the operator's YAML index")
	require.NotNil(t, plan)
	plan.Apply()
	assert.Equal(t, []string{"k0", "k1", "b-new", "k3"}, ringKeys(f.keyrings["openai"]))
}

func TestKeyRotationKeepsResolvedKeysContainingPlaceholderText(t *testing.T) {
	f := newRotationFixture(t, map[string]string{"a": "a-old"},
		map[string]config.RawProviderConfig{"openai": {Type: "openai", APIKey: "${vault:a}"}},
		nil)
	f.vault.set("a", "sk-${not-a-placeholder}")
	_, plan := f.plan(t)
	require.NotNil(t, plan, "a resolved secret is data, not a placeholder")
	plan.Apply()
	assert.Equal(t, []string{"sk-${not-a-placeholder}"}, ringKeys(f.keyrings["openai"]))
}

func TestKeyRotationIgnoresProvidersSkippedForMissingCredentials(t *testing.T) {
	// A provider whose only key is an unset legacy ${VAR} is dropped before
	// resolution, so its other references are never looked up or tracked.
	f := newRotationFixture(t, map[string]string{"a": "a-old", "url": "https://a.test/v1"},
		map[string]config.RawProviderConfig{
			"openai":  {Type: "openai", APIKey: "${vault:a}"},
			"skipped": {Type: "openai", APIKey: "${GOMODEL_TEST_UNSET_KEY}", BaseURL: "${vault:url}"},
		},
		nil)
	f.vault.set("url", "https://b.test/v1")
	f.vault.set("a", "a-new")
	recheck, plan := f.plan(t)
	assert.Equal(t, []string{"providers.openai.api_key"}, recheck.Fields())
	require.NotNil(t, plan)
	assert.Equal(t, []string{"openai"}, plan.Providers())
}

func TestKeyRotationPatchesKeysCollapsedOrDroppedAtStartup(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		raw    config.RawProviderConfig
		rotate string
		want   []string
	}{
		{
			name:   "two references that resolved to one key split apart",
			values: map[string]string{"a": "same", "b": "same"},
			raw:    config.RawProviderConfig{Type: "openai", APIKey: "${vault:a}", APIKeys: []string{"${vault:b}"}},
			rotate: "b",
			want:   []string{"same", "b-new"},
		},
		{
			name:   "a reference that resolved empty gains a value",
			values: map[string]string{"b": ""},
			raw:    config.RawProviderConfig{Type: "openai", APIKey: "k0", APIKeys: []string{"${vault:b}"}},
			rotate: "b",
			want:   []string{"k0", "b-new"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRotationFixture(t, tt.values, map[string]config.RawProviderConfig{"openai": tt.raw}, nil)
			f.vault.set(tt.rotate, "b-new")
			recheck, plan := f.plan(t)
			assert.Equal(t, []string{"providers.openai.api_keys[0]"}, recheck.Fields())
			require.NotNil(t, plan)
			plan.Apply()
			assert.Equal(t, tt.want, ringKeys(f.keyrings["openai"]))
		})
	}
}
