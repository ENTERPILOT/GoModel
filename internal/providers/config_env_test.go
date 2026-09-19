package providers

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

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

// Named trip-rule env groups override config rules by name only; a malformed
// value must fail resilience validation instead of silently dropping quota
// protection.
func TestApplyProviderEnvVars_TripOnGroups(t *testing.T) {
	oneMinute := time.Minute

	setEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_QUOTA_EXCEEDED_MATCH", "quota exceeded")
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_QUOTA_EXCEEDED_TTL", "15m")
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_USAGE_LIMIT_MATCH", "usage limit")
	}
	wantGroups := config.TripRuleMap{
		"QUOTA_EXCEEDED": {Name: "QUOTA_EXCEEDED", Match: "quota exceeded", TTL: 15 * time.Minute},
		"USAGE_LIMIT":    {Name: "USAGE_LIMIT", Match: "usage limit"},
	}

	t.Run("bare type env creates provider with parsed trip rules", func(t *testing.T) {
		setEnv(t)
		got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, map[string]DiscoveryConfig{
			"kimicode": {DefaultBaseURL: "https://api.kimi.com/coding/v1"},
		})

		p, ok := got["kimicode"]
		require.True(t, ok, "kimicode provider missing")
		require.NotNil(t, p.Resilience, "resilience overlay missing")
		require.NotNil(t, p.Resilience.CircuitBreaker, "circuit_breaker overlay missing")
		assert.Equal(t, wantGroups, p.Resilience.CircuitBreaker.TripOn)
	})

	t.Run("env group overrides only the config rule of the same name", func(t *testing.T) {
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_QUOTA_EXCEEDED_MATCH", "quota exhausted")
		existing := map[string]config.RawProviderConfig{
			"kimicode": {
				Type: "kimicode",
				Resilience: &config.RawResilienceConfig{
					CircuitBreaker: &config.RawCircuitBreakerConfig{
						Timeout: &oneMinute,
						TripOn: config.TripRuleMap{
							"quota_exceeded": {Match: "old pattern", TTL: time.Hour},
							"other_rule":     {Match: "yaml only", TTL: 2 * time.Hour},
						},
					},
				},
			},
		}
		got := applyProviderEnvVars(existing, map[string]DiscoveryConfig{"kimicode": {}})

		p := got["kimicode"]
		require.NotNil(t, p.Resilience)
		require.NotNil(t, p.Resilience.CircuitBreaker)
		assert.Equal(t, time.Minute, *p.Resilience.CircuitBreaker.Timeout, "YAML timeout must survive the env overlay")
		tripOn := p.Resilience.CircuitBreaker.TripOn
		assert.Equal(t, config.TripRuleConfig{Name: "QUOTA_EXCEEDED", Match: "quota exhausted"}, tripOn["QUOTA_EXCEEDED"], "same-named group replaces the config rule")
		assert.Equal(t, config.TripRuleConfig{Name: "other_rule", Match: "yaml only", TTL: 2 * time.Hour}, tripOn["other_rule"], "unnamed-by-env rule survives")
	})

	t.Run("renamed provider with YAML trip_on ignores the env value", func(t *testing.T) {
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_QUOTA_MATCH", "quota exceeded")
		yamlRules := config.TripRuleMap{"yaml": {Match: "yaml rule"}}
		existing := map[string]config.RawProviderConfig{
			"kimi-renamed": {
				Type: "kimicode",
				Resilience: &config.RawResilienceConfig{
					CircuitBreaker: &config.RawCircuitBreakerConfig{TripOn: yamlRules},
				},
			},
		}
		logs := captureSlog(t)
		got := applyProviderEnvVars(existing, map[string]DiscoveryConfig{"kimicode": {}})

		p := got["kimi-renamed"]
		assert.Equal(t, yamlRules, p.Resilience.CircuitBreaker.TripOn, "YAML rules must win over env")
		assert.Contains(t, logs.String(), "trip_on")
	})

	t.Run("malformed ttl fails resilience validation", func(t *testing.T) {
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_BAD_MATCH", "quota exceeded")
		t.Setenv("KIMICODE_CIRCUIT_BREAKER_TRIP_ON_BAD_TTL", "banana")
		got := applyProviderEnvVars(map[string]config.RawProviderConfig{}, map[string]DiscoveryConfig{
			"kimicode": {DefaultBaseURL: "https://api.kimi.com/coding/v1"},
		})

		p := got["kimicode"]
		require.NotNil(t, p.Resilience)
		require.Error(t, config.ValidateResilience(config.ResilienceConfig{
			CircuitBreaker: config.CircuitBreakerConfig{TripOn: p.Resilience.CircuitBreaker.TripOn},
		}), "malformed TRIP_ON group must fail validation")
	})
}
