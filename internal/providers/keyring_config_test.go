package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveKeys is a shorthand for the API key set the given provider ends up with.
func resolveKeys(t *testing.T, raw map[string]config.RawProviderConfig, provider string) ProviderConfig {
	t.Helper()
	got, _ := resolveProviders(raw, globalResilience, testDiscoveryConfigs)
	cfg, ok := got[provider]
	require.True(t, ok, "provider %q not resolved; got %v", provider, got)

	return cfg
}

func TestResolveProviders_NumberedAPIKeyEnvVars(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{{
		name: "unsuffixed key plus a numbered one",
		env:  map[string]string{"OPENAI_API_KEY": "a", "OPENAI_API_KEY_2": "b"},
		want: []string{"a", "b"},
	}, {
		name: "only a numbered key still configures the provider",
		env:  map[string]string{"OPENAI_API_KEY_2": "b"},
		want: []string{"b"},
	}, {
		name: "gaps in the numbering are ignored",
		env:  map[string]string{"OPENAI_API_KEY": "a", "OPENAI_API_KEY_3": "c"},
		want: []string{"a", "c"},
	}, {
		name: "every slot spelled out, no unsuffixed key",
		env:  map[string]string{"OPENAI_API_KEY_1": "a", "OPENAI_API_KEY_2": "b"},
		want: []string{"a", "b"},
	}, {
		name: "the unsuffixed key leads the numbered ones",
		env:  map[string]string{"OPENAI_API_KEY_1": "b", "OPENAI_API_KEY": "a"},
		want: []string{"a", "b"},
	}, {
		name: "a key repeated across slots is used once",
		env:  map[string]string{"OPENAI_API_KEY": "a", "OPENAI_API_KEY_1": "a"},
		want: []string{"a"},
	}, {
		// Guards against sorting the indexes as strings, which would order
		// slot 10 ahead of slot 2.
		name: "slots are ordered numerically, not lexically",
		env:  map[string]string{"OPENAI_API_KEY": "a", "OPENAI_API_KEY_2": "b", "OPENAI_API_KEY_10": "j"},
		want: []string{"a", "b", "j"},
	}, {
		name: "slot zero is not a rotation slot",
		env:  map[string]string{"OPENAI_API_KEY": "a", "OPENAI_API_KEY_0": "ignored"},
		want: []string{"a"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "")
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			cfg := resolveKeys(t, map[string]config.RawProviderConfig{}, "openai")

			assert.True(t, equalStrings(cfg.APIKeys, tt.want), "APIKeys = %v, want %v", cfg.APIKeys, tt.want)
			assert.Equal(t, tt.want[0], cfg.APIKey)

			// A provider configured only through a numbered key still needs its
			// default endpoint.
			assert.Equal(t, "https://api.openai.com/v1", cfg.BaseURL)
		})
	}
}

// `OPENAI_EU_API_KEY_2` is key 2 of provider `openai-eu`, while
// `OPENAI_REGION_2_API_KEY` is the only key of provider `openai-region-2`.
// The trailing digits mean different things and must not be conflated.
func TestResolveProviders_NumberedKeysOnSuffixedProviders(t *testing.T) {
	t.Run("numbered key on a suffixed provider", func(t *testing.T) {
		t.Setenv("OPENAI_EU_API_KEY", "a")
		t.Setenv("OPENAI_EU_API_KEY_2", "b")

		cfg := resolveKeys(t, map[string]config.RawProviderConfig{}, "openai-eu")
		want := []string{"a", "b"}
		assert.True(t, equalStrings(cfg.APIKeys, want), "APIKeys = %v, want %v", cfg.APIKeys, want)
	})

	t.Run("suffix ending in a digit is not a rotation slot", func(t *testing.T) {
		t.Setenv("OPENAI_REGION_2_API_KEY", "a")

		cfg := resolveKeys(t, map[string]config.RawProviderConfig{}, "openai-region-2")
		want := []string{"a"}
		assert.True(t, equalStrings(cfg.APIKeys, want), "APIKeys = %v, want %v", cfg.APIKeys, want)
	})
}

func TestResolveProviders_APIKeysFromYAML(t *testing.T) {
	tests := []struct {
		name string
		raw  config.RawProviderConfig
		want []string
	}{{
		name: "api_keys list alone",
		raw:  config.RawProviderConfig{Type: "openai", APIKeys: []string{"a", "b"}},
		want: []string{"a", "b"},
	}, {
		name: "api_key leads the api_keys list",
		raw:  config.RawProviderConfig{Type: "openai", APIKey: "a", APIKeys: []string{"b"}},
		want: []string{"a", "b"},
	}, {
		name: "api_key repeated inside api_keys is used once",
		raw:  config.RawProviderConfig{Type: "openai", APIKey: "a", APIKeys: []string{"a", "b"}},
		want: []string{"a", "b"},
	}, {
		name: "unresolved placeholders are dropped, not sent as credentials",
		raw:  config.RawProviderConfig{Type: "openai", APIKey: "a", APIKeys: []string{"${OPENAI_API_KEY_2}"}},
		want: []string{"a"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "")

			cfg := resolveKeys(t, map[string]config.RawProviderConfig{"openai": tt.raw}, "openai")

			assert.True(t, equalStrings(cfg.APIKeys, tt.want), "APIKeys = %v, want %v", cfg.APIKeys, tt.want)
			assert.Equal(t, tt.want[0], cfg.APIKey)
		})
	}
}

// hasAPIKey probes the env fields directly instead of building the ordered key
// list. A value that cannot serve as a credential -- whitespace, or an
// unresolved `${VAR}` -- must read as "no key", not merely "field is set".
func TestProviderEnvValues_HasAPIKeyMatchesAPIKeys(t *testing.T) {
	tests := []struct {
		name string
		v    providerEnvValues
		want bool
	}{
		{name: "no fields set"},
		{name: "blank unsuffixed key", v: providerEnvValues{APIKey: "  "}},
		{name: "unresolved unsuffixed key", v: providerEnvValues{APIKey: "${MISSING}"}},
		{name: "blank numbered key", v: providerEnvValues{APIKeysByIndex: map[int]string{2: "   "}}},
		{name: "unresolved numbered key", v: providerEnvValues{APIKeysByIndex: map[int]string{2: "${MISSING}"}}},
		{name: "usable unsuffixed key", v: providerEnvValues{APIKey: "a"}, want: true},
		{name: "usable numbered key only", v: providerEnvValues{APIKeysByIndex: map[int]string{2: "b"}}, want: true},
		{
			name: "unresolved unsuffixed key beside a usable numbered one",
			v:    providerEnvValues{APIKey: "${MISSING}", APIKeysByIndex: map[int]string{2: "b"}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.v.hasAPIKey()
			assert.Equal(t, tt.want, got)

			// The cheap probe must never disagree with the full key list.
			assert.Equal(t, len(tt.v.apiKeys()) > 0, tt.v.hasAPIKey(), "hasAPIKey() disagrees with apiKeys()")
		})
	}
}

// A provider whose only key is an unresolved placeholder has no credentials at
// all and must be dropped, exactly as before rotation existed.
func TestResolveProviders_ProviderWithOnlyUnresolvedKeysIsDropped(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
	}
	got, _ := resolveProviders(raw, globalResilience, testDiscoveryConfigs)
	_, ok := got["openai"]
	assert.False(t, ok)
}

// Env replaces the provider's whole key set rather than merging into it, so a
// key removed from the environment stops being used.
func TestResolveProviders_EnvKeysReplaceYAMLKeySet(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-a")
	t.Setenv("OPENAI_API_KEY_2", "env-b")

	raw := map[string]config.RawProviderConfig{
		"openai": {Type: "openai", APIKeys: []string{"yaml-a", "yaml-b", "yaml-c"}},
	}
	cfg := resolveKeys(t, raw, "openai")
	want := []string{"env-a", "env-b"}
	assert.True(t, equalStrings(cfg.APIKeys, want), "APIKeys = %v, want %v", cfg.APIKeys, want)
}

// The factory hands every provider one shared ring, so all the clients a
// provider builds rotate together.
func TestProviderFactory_CreateBuildsKeyring(t *testing.T) {
	var got ProviderOptions
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "openai",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			got = opts
			return nil
		},
	})
	_, err := factory.Create(ProviderConfig{Type: "openai", APIKey: "a", APIKeys: []string{"a", "b"}})
	require.NoError(t, err)
	require.Equal(t, 2, got.Keys.Len())
	assert.True(t, got.Keys.Rotates())
	key := // The provider's own constructor key must not override the factory ring.
		got.Keyring("a").Next()
	assert.Equal(t, "a", key)
	key = got.Keyring("a").Next()
	assert.Equal(t, "b", key)
}
