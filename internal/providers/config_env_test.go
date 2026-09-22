package providers

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSlog routes the default logger into a buffer for the test's lifetime.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &buf
}

func TestApplyProviderEnvVars_BareTypeEnvVarsAgainstRenamedProviders(t *testing.T) {
	const envKey = "sk-env-secret-key"

	tests := []struct {
		name         string
		env          map[string]string
		raw          map[string]config.RawProviderConfig
		want         map[string]config.RawProviderConfig
		wantLog      []string // substrings the warning must contain
		wantNoOpenAI bool
	}{
		{
			name: "renamed provider keeps its explicit api_key and base_url",
			env:  map[string]string{"OPENAI_API_KEY": envKey, "OPENAI_BASE_URL": "https://env.example.com/v1"},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "http://localhost:9001/v1"},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "http://localhost:9001/v1"},
			},
			wantLog:      []string{`"env_prefix":"OPENAI"`, `"provider":"alpha"`, `"api_key"`, `"base_url"`},
			wantNoOpenAI: true,
		},
		{
			name: "renamed provider with empty api_key receives the env key",
			env:  map[string]string{"OPENAI_API_KEY": envKey},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", BaseURL: "https://proxy.example.com/v1"},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: envKey, APIKeys: []string{envKey}, BaseURL: "https://proxy.example.com/v1"},
			},
			wantNoOpenAI: true,
		},
		{
			name: "renamed provider with unresolved placeholder api_key receives the env key",
			env:  map[string]string{"OPENAI_API_KEY": envKey},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "${MISSING_KEY}"},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: envKey, APIKeys: []string{envKey}, BaseURL: testDiscoveryConfigs["openai"].DefaultBaseURL},
			},
			wantNoOpenAI: true,
		},
		{
			name: "renamed provider with an embedded base_url placeholder receives the env base_url",
			env:  map[string]string{"OPENAI_BASE_URL": "https://env.example.com/v1"},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://${MISSING_HOST}/v1"},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://env.example.com/v1"},
			},
			wantNoOpenAI: true,
		},
		{
			name: "renamed provider with unresolved model placeholders receives the env models",
			env:  map[string]string{"OPENAI_MODELS": "gpt-4o-mini,gpt-4o"},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://alpha.example.com/v1", Models: []config.RawProviderModel{{ID: "${MISSING_MODELS}"}}},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://alpha.example.com/v1", Models: []config.RawProviderModel{{ID: "gpt-4o-mini"}, {ID: "gpt-4o"}}},
			},
			wantNoOpenAI: true,
		},
		{
			name: "provider named after the type is fully overridden",
			env:  map[string]string{"OPENAI_API_KEY": envKey},
			raw: map[string]config.RawProviderConfig{
				"openai": {Type: "openai", APIKey: "yaml-key", BaseURL: "https://yaml.example.com/v1"},
			},
			want: map[string]config.RawProviderConfig{
				"openai": {Type: "openai", APIKey: envKey, APIKeys: []string{envKey}, BaseURL: "https://yaml.example.com/v1"},
			},
		},
		{
			name: "two same-type providers keep their keys and the env key is warned about",
			env:  map[string]string{"OPENAI_API_KEY": envKey},
			raw: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://alpha.example.com/v1"},
				"beta":  {Type: "openai", APIKey: "beta-key", BaseURL: "https://beta.example.com/v1"},
			},
			want: map[string]config.RawProviderConfig{
				"alpha": {Type: "openai", APIKey: "alpha-key", BaseURL: "https://alpha.example.com/v1"},
				"beta":  {Type: "openai", APIKey: "beta-key", BaseURL: "https://beta.example.com/v1"},
			},
			wantLog:      []string{`"env_prefix":"OPENAI"`, `"providers":["alpha","beta"]`},
			wantNoOpenAI: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			logs := captureSlog(t)

			got := applyProviderEnvVars(tt.raw, testDiscoveryConfigs)

			for name, want := range tt.want {
				p, ok := got[name]
				require.True(t, ok, "provider %q missing from result", name)
				assert.Equal(t, want.APIKey, p.APIKey, "%s APIKey", name)
				assert.Equal(t, strings.Join(want.APIKeys, ","), strings.Join(p.APIKeys, ","), "%s APIKeys", name)
				assert.Equal(t, want.BaseURL, p.BaseURL, "%s BaseURL", name)
				assert.Equal(t, strings.Join(config.ProviderModelIDs(want.Models), ","), strings.Join(config.ProviderModelIDs(p.Models), ","), "%s Models", name)
			}
			if tt.wantNoOpenAI {
				_, exists := got["openai"]
				assert.False(t, exists)
			}

			out := logs.String()
			if len(tt.wantLog) == 0 {
				assert.Empty(t, out, "expected no warning")
			}
			for _, fragment := range tt.wantLog {
				assert.Contains(t, out, fragment)
			}
			assert.NotContains(t, out, envKey, "warning leaked the env api key")
		})
	}
}

func TestApplyProviderEnvVars_ProxyURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENAI_PROXY_URL", "http://proxy.internal:3128")
	t.Setenv("OPENAI_EU_API_KEY", "sk-eu")
	t.Setenv("OPENAI_EU_PROXY_URL", "socks5://user:pass@10.0.0.1:1080")

	got := applyProviderEnvVars(map[string]config.RawProviderConfig{
		"anthropic": {Type: "anthropic", APIKey: "sk-ant", ProxyURL: "http://yaml-proxy:3128"},
	}, testDiscoveryConfigs)

	require.Contains(t, got, "openai")
	assert.Equal(t, "http://proxy.internal:3128", got["openai"].ProxyURL)
	require.Contains(t, got, "openai-eu")
	assert.Equal(t, "socks5://user:pass@10.0.0.1:1080", got["openai-eu"].ProxyURL)
	// YAML keeps its proxy when the environment sets none for that provider.
	assert.Equal(t, "http://yaml-proxy:3128", got["anthropic"].ProxyURL)

	resolved := buildProviderConfig(got["openai-eu"], config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()})
	assert.Equal(t, "socks5://user:pass@10.0.0.1:1080", resolved.ProxyURL)
}
