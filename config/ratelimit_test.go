package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRateLimitEnvCompactSyntax(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_TEAM__ALPHA", "rpm=100,tpm=50000,rpd=1000,concurrent=10")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		entries := result.Config.RateLimits.UserPaths
		if len(entries) != 1 {
			t.Fatalf("expected 1 rate limit user path, got %d", len(entries))
		}
		if got, want := entries[0].Path, "/team/alpha"; got != want {
			t.Fatalf("rate limit env path = %q, want %q", got, want)
		}
		limits := entries[0].Limits
		if len(limits) != 3 {
			t.Fatalf("expected 3 limits (minute, day, concurrent), got %d: %+v", len(limits), limits)
		}

		byPeriod := map[int64]RateLimitRuleConfig{}
		for _, limit := range limits {
			if limit.PeriodSeconds == nil {
				t.Fatalf("limit %+v missing resolved period seconds", limit)
			}
			byPeriod[*limit.PeriodSeconds] = limit
		}
		minute := byPeriod[60]
		if minute.MaxRequests == nil || *minute.MaxRequests != 100 {
			t.Fatalf("minute max_requests = %v, want 100", minute.MaxRequests)
		}
		if minute.MaxTokens == nil || *minute.MaxTokens != 50000 {
			t.Fatalf("minute max_tokens = %v, want 50000", minute.MaxTokens)
		}
		day := byPeriod[86400]
		if day.MaxRequests == nil || *day.MaxRequests != 1000 || day.MaxTokens != nil {
			t.Fatalf("day limit = %+v, want 1000 requests only", day)
		}
		concurrent := byPeriod[0]
		if concurrent.MaxRequests == nil || *concurrent.MaxRequests != 10 {
			t.Fatalf("concurrent limit = %+v, want 10", concurrent)
		}
	})
}

func TestLoadRateLimitEnvJSONSyntax(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_", `[{"period":"minute","max_requests":50},{"period_seconds":7200,"max_tokens":900}]`)

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		entries := result.Config.RateLimits.UserPaths
		if len(entries) != 1 || entries[0].Path != "/" {
			t.Fatalf("entries = %+v, want single root entry", entries)
		}
		limits := entries[0].Limits
		if len(limits) != 2 {
			t.Fatalf("limits = %d, want 2", len(limits))
		}
		if limits[0].PeriodSeconds == nil || *limits[0].PeriodSeconds != 60 || *limits[0].MaxRequests != 50 {
			t.Fatalf("first limit = %+v, want minute/50", limits[0])
		}
		if limits[1].PeriodSeconds == nil || *limits[1].PeriodSeconds != 7200 || *limits[1].MaxTokens != 900 {
			t.Fatalf("second limit = %+v, want 7200s/900 tokens", limits[1])
		}
	})
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
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlConfig), 0644); err != nil {
			t.Fatalf("Failed to write config.yaml: %v", err)
		}
		t.Setenv("SET_RATE_LIMIT_TEAM__ALPHA", "rpm=50")

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		entries := result.Config.RateLimits.UserPaths
		if len(entries) != 2 {
			t.Fatalf("expected 2 rate limit user paths, got %d: %+v", len(entries), entries)
		}
		if entries[0].Path != "/team/beta" || *entries[0].Limits[0].MaxRequests != 2 {
			t.Fatalf("unrelated YAML entry changed: %+v", entries[0])
		}
		if entries[1].Path != "/team/alpha" || *entries[1].Limits[0].MaxRequests != 50 {
			t.Fatalf("env entry did not replace YAML entry: %+v", entries[1])
		}
	})
}

func TestLoadRateLimitEnvRejectsUnknownName(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		t.Setenv("SET_RATE_LIMIT_TEAM", "rps=10")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "rps") {
			t.Fatalf("Load() error = %v, want unknown-name error naming rps", err)
		}
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
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tt.yaml), 0644); err != nil {
					t.Fatalf("Failed to write config.yaml: %v", err)
				}
				_, err := Load()
				if tt.wantErr == "" {
					if err != nil {
						t.Fatalf("Load() failed: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
			})
		})
	}
}

func TestRateLimitsEnabledByDefaultAndTogglable(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if !result.Config.RateLimits.Enabled {
			t.Fatal("rate limits should be enabled by default")
		}
	})

	withTempDir(t, func(string) {
		t.Setenv("RATE_LIMITS_ENABLED", "false")
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if result.Config.RateLimits.Enabled {
			t.Fatal("RATE_LIMITS_ENABLED=false was not applied")
		}
	})
}
