package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRateLimitEnvCompactSyntax(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_TEAM__ALPHA", "rpm=100,tpm=50000,rpd=1000,concurrent=10")

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.RateLimits.UserPaths
		require.Len(t, entries, 1)
		require.Equal(t, "/team/alpha", entries[0].Path)
		limits := entries[0].Limits
		require.Len(t, limits, 3)

		byPeriod := map[int64]RateLimitRuleConfig{}
		for _, limit := range limits {
			require.NotNil(t, limit.PeriodSeconds, "limit %+v missing resolved period seconds", limit)

			byPeriod[*limit.PeriodSeconds] = limit
		}
		minute := byPeriod[60]
		require.NotNil(t, minute.MaxRequests)
		require.Equal(t, int64(100), *minute.MaxRequests)
		require.NotNil(t, minute.MaxTokens)
		require.Equal(t, int64(50000), *minute.MaxTokens)

		day := byPeriod[86400]
		require.NotNil(t, day.MaxRequests)
		require.Equal(t, int64(1000), *day.MaxRequests)
		require.Nil(t, day.MaxTokens, "day limit = %+v, want 1000 requests only", day)

		concurrent := byPeriod[0]
		require.NotNil(t, concurrent.MaxRequests)
		require.Equal(t, int64(10), *concurrent.MaxRequests, "concurrent limit = %+v, want 10", concurrent)
	})
}

func TestLoadRateLimitEnvJSONSyntax(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_", `[{"period":"minute","max_requests":50,"per_child":true},{"period_seconds":7200,"max_tokens":900}]`)

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.RateLimits.UserPaths
		require.Len(t, entries, 1)
		require.Equal(t, "/", entries[0].Path)

		limits := entries[0].Limits
		require.Len(t, limits, 2)
		require.NotNil(t, limits[0].PeriodSeconds)
		require.Equal(t, int64(60), *limits[0].PeriodSeconds)
		require.Equal(t, int64(50), *limits[0].MaxRequests, "first limit = %+v, want minute/50", limits[0])
		require.True(t, limits[0].PerChild)
		require.NotNil(t, limits[1].PeriodSeconds)
		require.Equal(t, int64(7200), *limits[1].PeriodSeconds)
		require.Equal(t, int64(900), *limits[1].MaxTokens, "second limit = %+v, want 7200s/900 tokens", limits[1])
	})
}

func TestLoadRateLimitYAMLSupportsPerChild(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
rate_limits:
  user_paths:
    - path: /customers
      per_child: true
      limits:
        - period: minute
          max_requests: 100
`
		result := loadConfigYAML(t, dir, yamlConfig)

		entry := result.Config.RateLimits.UserPaths[0]
		require.Equal(t, "/customers", entry.Path)
		require.True(t, entry.PerChild, "rate-limit YAML entry = %+v, want per-child /customers", entry)
	})
}

func TestValidateRateLimitConfigRejectsPerChildOutsideUserPaths(t *testing.T) {
	tests := []struct {
		name string
		cfg  RateLimitsConfig
		want string
	}{
		{
			name: "provider",
			cfg: RateLimitsConfig{Enabled: true, Providers: []RateLimitProviderConfig{{
				Name:   "openai",
				Limits: []RateLimitRuleConfig{{Period: "minute", MaxRequests: new(int64(1)), PerChild: true}},
			}}},
			want: "rate_limits.providers[0].limits[0].per_child is only valid for user_path rules",
		},
		{
			name: "model",
			cfg: RateLimitsConfig{Enabled: true, Models: []RateLimitModelConfig{{
				Model:  "gpt-4o",
				Limits: []RateLimitRuleConfig{{Period: "minute", MaxRequests: new(int64(1)), PerChild: true}},
			}}},
			want: "rate_limits.models[0].limits[0].per_child is only valid for user_path rules",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRateLimitConfig(&tt.cfg)
			require.Error(t, err)
			require.Equal(t, tt.want, err.Error())
		})
	}
}

func TestLoadRateLimitEnvReplacesMatchingYAMLUserPath(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
rate_limits:
  user_paths:
    - path: /team/alpha
      limits:
        - period: minute
          max_requests: 1
    - path: /team/beta
      limits:
        - period: minute
          max_requests: 2
`
		writeConfigYAML(t, dir, yamlConfig)

		t.Setenv("SET_RATE_LIMIT_TEAM__ALPHA", "rpm=50")

		result, err := Load()
		require.NoError(t, err)

		entries := result.Config.RateLimits.UserPaths
		require.Len(t, entries, 2)
		require.Equal(t, "/team/beta", entries[0].Path)
		require.Equal(t, int64(2), *entries[0].Limits[0].MaxRequests, "unrelated YAML entry changed: %+v", entries[0])
		require.Equal(t, "/team/alpha", entries[1].Path)
		require.Equal(t, int64(50), *entries[1].Limits[0].MaxRequests, "env entry did not replace YAML entry: %+v", entries[1])
	})
}

func TestLoadProviderRateLimitEnv(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(dir string) {
		yamlConfig := `
rate_limits:
  providers:
    - name: openai
      limits:
        - period: minute
          max_requests: 1
    - name: anthropic
      limits:
        - period: minute
          max_requests: 2
  models:
    - model: openai/gpt-4o
      limits:
        - period: minute
          max_tokens: 90000
`
		writeConfigYAML(t, dir, yamlConfig)

		// The env entry replaces the whole YAML entry with the same provider,
		// and the distinct prefix keeps it out of the user-path suffix space.
		t.Setenv("SET_PROVIDER_RATE_LIMIT_OPENAI", "rpm=500,tpm=100000,concurrent=20")
		// Underscores map to hyphens like provider-instance env vars.
		t.Setenv("SET_PROVIDER_RATE_LIMIT_OPENAI_EAST", "rpm=100")

		result, err := Load()
		require.NoError(t, err)

		providers := result.Config.RateLimits.Providers
		require.Len(t, providers, 3)

		byName := map[string]RateLimitProviderConfig{}
		for _, entry := range providers {
			byName[entry.Name] = entry
		}
		entry := byName["anthropic"]
		require.Equal(t, int64(2), *entry.Limits[0].MaxRequests, "unrelated YAML provider changed: %+v", entry)
		entry = byName["openai"]
		require.Len(t, entry.Limits, 2, "env provider entry = %+v, want openai with minute+concurrent limits", entry)
		entry, ok := byName["openai-east"]
		require.True(t, ok)
		require.Equal(t, int64(100), *entry.Limits[0].MaxRequests, "providers = %+v, want openai-east from underscore suffix", providers)
		require.Nil(t, result.Config.RateLimits.UserPaths)

		models := result.Config.RateLimits.Models
		require.Len(t, models, 1)
		require.Equal(t, "openai/gpt-4o", models[0].Model)
		require.NotNil(t, models[0].Limits[0].PeriodSeconds)
		require.Equal(t, int64(60), *models[0].Limits[0].PeriodSeconds)
		require.Equal(t, int64(90000), *models[0].Limits[0].MaxTokens, "model limit = %+v, want minute/90000 tokens", models[0].Limits[0])
	})
}

func TestLoadRateLimitEnvRejectsUnknownName(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_TEAM", "rps=10")
		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "rps")
	})
}

func TestRateLimitConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid config",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: minute
          max_requests: 10
          max_tokens: 100
        - period: concurrent
          max_requests: 5
`,
		},
		{
			name: "duplicate period",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: minute
          max_requests: 10
        - period_seconds: 60
          max_tokens: 100
`,
			wantErr: "duplicate rate limit",
		},
		{
			name: "concurrent with tokens",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: concurrent
          max_requests: 5
          max_tokens: 100
`,
			wantErr: "max_tokens is not valid for the concurrent period",
		},
		{
			name: "windowed rule without limits",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: minute
`,
			wantErr: "requires max_requests or max_tokens",
		},
		{
			name: "unknown period",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: fortnight
          max_requests: 5
`,
			wantErr: "period must be one of",
		},
		{
			name: "negative max_requests",
			yaml: `
rate_limits:
  user_paths:
    - path: /team
      limits:
        - period: minute
          max_requests: -1
`,
			wantErr: "max_requests must be greater than 0",
		},
		{
			name: "valid provider and model rules",
			yaml: `
rate_limits:
  providers:
    - name: OpenAI
      limits:
        - period: minute
          max_requests: 500
  models:
    - model: openai/gpt-4o
      limits:
        - period: minute
          max_tokens: 90000
`,
		},
		{
			name: "provider name required",
			yaml: `
rate_limits:
  providers:
    - name: "  "
      limits:
        - period: minute
          max_requests: 5
`,
			wantErr: "providers[0].name is required",
		},
		{
			name: "provider name rejects slashes",
			yaml: `
rate_limits:
  providers:
    - name: open/ai
      limits:
        - period: minute
          max_requests: 5
`,
			wantErr: "without slashes or spaces",
		},
		{
			name: "model subject required",
			yaml: `
rate_limits:
  models:
    - model: ""
      limits:
        - period: minute
          max_requests: 5
`,
			wantErr: "models[0].model is required",
		},
		{
			name: "duplicate provider period",
			yaml: `
rate_limits:
  providers:
    - name: openai
      limits:
        - period: minute
          max_requests: 5
        - period_seconds: 60
          max_tokens: 100
`,
			wantErr: "duplicate rate limit",
		},
		{
			name: "disabled config skips validation",
			yaml: `
rate_limits:
  enabled: false
  user_paths:
    - path: /team
      limits:
        - period: fortnight
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			withTempDir(t, func(dir string) {
				writeConfigYAML(t, dir, tt.yaml)

				_, err := Load()
				if tt.wantErr == "" {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
			})
		})
	}
}

func TestRateLimitsEnabledByDefaultAndTogglable(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.True(t, result.Config.RateLimits.Enabled)
		require.Equal(t, 1, result.Config.RateLimits.FlushInterval)
	})

	withTempDir(t, func(string) {
		t.Setenv("RATE_LIMITS_ENABLED", "false")
		result, err := Load()
		require.NoError(t, err)
		require.False(t, result.Config.RateLimits.Enabled)
	})
}

func TestRateLimitsFlushIntervalEnv(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("RATE_LIMITS_FLUSH_INTERVAL", "0")
		result, err := Load()
		require.NoError(t, err)
		require.Equal(t, 0, result.Config.RateLimits.FlushInterval)
	})

	clearAllConfigEnvVars(t)
	withTempDir(t, func(string) {
		t.Setenv("RATE_LIMITS_FLUSH_INTERVAL", "-1")
		_, err := Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "flush_interval")
	})
}

func TestParseRateLimitEnvLimits_RejectsUnknownField(t *testing.T) {
	_, err := parseRateLimitEnvLimits(`[{"period":"minute","max_requsts":100}]`, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_requsts")
}
