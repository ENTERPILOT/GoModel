package config

import (
	"strings"
	"testing"
)

// The GOMODEL_ prefix is the canonical spelling for every GoModel-defined
// variable; the bare spelling is deprecated but still honored. These tests
// exercise the four resolution paths that reach the environment, since each
// one reads it differently:
//
//   - struct `env` tags, via applyEnvOverrides
//   - named reads outside the tag walker (GOMODEL_CONFIG_STRICT, JSON blobs)
//   - prefix scans over os.Environ (GOMODEL_SET_RATE_LIMIT_*, GOMODEL_SET_BUDGET_*)
//   - prefix scan plus companion lookups (GOMODEL_TAGGING_HEADER_<N>_PREFIX)

func TestEnvPrefixStructTags(t *testing.T) {
	t.Run("canonical spelling applies", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_SQLITE_PATH", "/canonical.db")
		t.Setenv("GOMODEL_STORAGE_TYPE", "sqlite")

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if got := cfg.Storage.SQLite.Path; got != "/canonical.db" {
			t.Errorf("SQLite.Path = %q, want %q", got, "/canonical.db")
		}
		if got := cfg.Storage.Type; got != "sqlite" {
			t.Errorf("Storage.Type = %q, want %q", got, "sqlite")
		}
	})

	t.Run("legacy spelling still applies", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("SQLITE_PATH", "/legacy.db")

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if got := cfg.Storage.SQLite.Path; got != "/legacy.db" {
			t.Errorf("SQLite.Path = %q, want %q", got, "/legacy.db")
		}
	})

	t.Run("canonical wins when both are set", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_SQLITE_PATH", "/canonical.db")
		t.Setenv("SQLITE_PATH", "/legacy.db")

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if got := cfg.Storage.SQLite.Path; got != "/canonical.db" {
			t.Errorf("SQLite.Path = %q, want %q (canonical must win)", got, "/canonical.db")
		}
	})

	t.Run("non-string kinds resolve through the prefix", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_LOGGING_ENABLED", "true")              // bool
		t.Setenv("GOMODEL_LOGGING_RETENTION_DAYS", "7")          // int
		t.Setenv("GOMODEL_ENABLED_PASSTHROUGH_PROVIDERS", "a,b") // []string

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if !cfg.Logging.Enabled {
			t.Error("Logging.Enabled = false, want true")
		}
		if got := cfg.Logging.RetentionDays; got != 7 {
			t.Errorf("Logging.RetentionDays = %d, want 7", got)
		}
		if got := cfg.Server.EnabledPassthroughProviders; len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("EnabledPassthroughProviders = %v, want [a b]", got)
		}
	})

	t.Run("exempt PORT stays bare", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("PORT", "9999")
		t.Setenv("GOMODEL_PORT", "1111")

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if got := cfg.Server.Port; got != "9999" {
			t.Errorf("Server.Port = %q, want %q (PORT is exempt; GOMODEL_PORT must be ignored)", got, "9999")
		}
	})

	t.Run("already-prefixed tag is not double-prefixed", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_MASTER_KEY", "sk-canonical")

		cfg := &Config{}
		if err := applyEnvOverrides(cfg); err != nil {
			t.Fatalf("applyEnvOverrides: %v", err)
		}
		if got := cfg.Server.MasterKey; got != "sk-canonical" {
			t.Errorf("Server.MasterKey = %q, want %q", got, "sk-canonical")
		}
	})
}

func TestEnvPrefixConfigStrict(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "default is strict", want: true},
		{name: "canonical spelling", env: map[string]string{"GOMODEL_CONFIG_STRICT": "false"}, want: false},
		{name: "legacy spelling", env: map[string]string{"CONFIG_STRICT": "false"}, want: false},
		{
			name: "canonical wins",
			env:  map[string]string{"GOMODEL_CONFIG_STRICT": "true", "CONFIG_STRICT": "false"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			t.Setenv("CONFIG_STRICT", "")
			t.Setenv("GOMODEL_CONFIG_STRICT", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := resolveConfigStrict()
			if err != nil {
				t.Fatalf("resolveConfigStrict: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveConfigStrict() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvPrefixVirtualModels(t *testing.T) {
	const payload = `[{"source":"gpt-4o","targets":[{"provider":"openai","model":"gpt-4o"}]}]`

	for _, spelling := range []string{"GOMODEL_VIRTUAL_MODELS", "VIRTUAL_MODELS"} {
		t.Run(spelling, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			t.Setenv("VIRTUAL_MODELS", "")
			t.Setenv("GOMODEL_VIRTUAL_MODELS", "")
			t.Setenv(spelling, payload)

			cfg := &Config{}
			if err := applyVirtualModelsEnv(cfg, true); err != nil {
				t.Fatalf("applyVirtualModelsEnv: %v", err)
			}
			if len(cfg.VirtualModels) != 1 || cfg.VirtualModels[0].Source != "gpt-4o" {
				t.Fatalf("VirtualModels = %+v, want one entry sourced gpt-4o", cfg.VirtualModels)
			}
		})
	}
}

func TestEnvPrefixRateLimitScan(t *testing.T) {
	t.Run("canonical spelling applies", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_SET_RATE_LIMIT_TEAM", "rpm=10")

		cfg := &Config{}
		cfg.RateLimits.Enabled = true
		if err := applyRateLimitEnv(cfg, true); err != nil {
			t.Fatalf("applyRateLimitEnv: %v", err)
		}
		if len(cfg.RateLimits.UserPaths) != 1 {
			t.Fatalf("UserPaths = %+v, want one entry", cfg.RateLimits.UserPaths)
		}
		if got := cfg.RateLimits.UserPaths[0].Path; got != "/team" {
			t.Errorf("Path = %q, want %q", got, "/team")
		}
	})

	t.Run("legacy spelling still applies", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("SET_RATE_LIMIT_TEAM", "rpm=10")

		cfg := &Config{}
		cfg.RateLimits.Enabled = true
		if err := applyRateLimitEnv(cfg, true); err != nil {
			t.Fatalf("applyRateLimitEnv: %v", err)
		}
		if len(cfg.RateLimits.UserPaths) != 1 {
			t.Fatalf("UserPaths = %+v, want one entry", cfg.RateLimits.UserPaths)
		}
	})

	t.Run("canonical wins over legacy for the same subject", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_SET_RATE_LIMIT_TEAM", "rpm=10")
		t.Setenv("SET_RATE_LIMIT_TEAM", "rpm=999")

		cfg := &Config{}
		cfg.RateLimits.Enabled = true
		if err := applyRateLimitEnv(cfg, true); err != nil {
			t.Fatalf("applyRateLimitEnv: %v", err)
		}
		if len(cfg.RateLimits.UserPaths) != 1 {
			t.Fatalf("UserPaths = %+v, want exactly one entry", cfg.RateLimits.UserPaths)
		}
		limits := cfg.RateLimits.UserPaths[0].Limits
		if len(limits) != 1 || limits[0].MaxRequests == nil || *limits[0].MaxRequests != 10 {
			t.Errorf("limits = %+v, want MaxRequests=10 (canonical must win)", limits)
		}
	})
}

func TestEnvPrefixTaggingHeaders(t *testing.T) {
	t.Run("canonical base and companions", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_TAGGING_HEADER_1", "X-Team")
		t.Setenv("GOMODEL_TAGGING_HEADER_1_PREFIX", "team-")
		t.Setenv("GOMODEL_TAGGING_HEADER_1_DONOTPASS", "true")
		t.Setenv("GOMODEL_TAGGING_HEADER_1_DELIMITER", "|")

		cfg := &Config{}
		if err := applyTaggingEnv(cfg); err != nil {
			t.Fatalf("applyTaggingEnv: %v", err)
		}
		if len(cfg.Tagging.Headers) != 1 {
			t.Fatalf("Headers = %+v, want one entry", cfg.Tagging.Headers)
		}
		h := cfg.Tagging.Headers[0]
		if h.Header != "X-Team" || h.Prefix != "team-" || !h.DoNotPass || h.Delimiter != "|" {
			t.Errorf("header = %+v, want {X-Team team- true |}", h)
		}
	})

	t.Run("legacy base and companions", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("TAGGING_HEADER_1", "X-Team")
		t.Setenv("TAGGING_HEADER_1_PREFIX", "team-")

		cfg := &Config{}
		if err := applyTaggingEnv(cfg); err != nil {
			t.Fatalf("applyTaggingEnv: %v", err)
		}
		if len(cfg.Tagging.Headers) != 1 {
			t.Fatalf("Headers = %+v, want one entry", cfg.Tagging.Headers)
		}
		if got := cfg.Tagging.Headers[0].Prefix; got != "team-" {
			t.Errorf("Prefix = %q, want %q", got, "team-")
		}
	})

	// Mixing spellings across a base and its companion is the case that breaks
	// if companions are resolved by string-concatenating the matched key rather
	// than by going back through envcompat.
	t.Run("canonical base with legacy companion", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_TAGGING_HEADER_1", "X-Team")
		t.Setenv("TAGGING_HEADER_1_PREFIX", "team-")

		cfg := &Config{}
		if err := applyTaggingEnv(cfg); err != nil {
			t.Fatalf("applyTaggingEnv: %v", err)
		}
		if len(cfg.Tagging.Headers) != 1 {
			t.Fatalf("Headers = %+v, want one entry", cfg.Tagging.Headers)
		}
		h := cfg.Tagging.Headers[0]
		if h.Header != "X-Team" || h.Prefix != "team-" {
			t.Errorf("header = %+v, want header X-Team with prefix team-", h)
		}
	})

	t.Run("legacy base with canonical companion", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("TAGGING_HEADER_1", "X-Team")
		t.Setenv("GOMODEL_TAGGING_HEADER_1_PREFIX", "team-")

		cfg := &Config{}
		if err := applyTaggingEnv(cfg); err != nil {
			t.Fatalf("applyTaggingEnv: %v", err)
		}
		if len(cfg.Tagging.Headers) != 1 {
			t.Fatalf("Headers = %+v, want one entry", cfg.Tagging.Headers)
		}
		if got := cfg.Tagging.Headers[0].Prefix; got != "team-" {
			t.Errorf("Prefix = %q, want %q", got, "team-")
		}
	})

	t.Run("companion alone does not create an entry", func(t *testing.T) {
		clearAllConfigEnvVars(t)
		t.Setenv("GOMODEL_TAGGING_HEADER_1_PREFIX", "team-")

		cfg := &Config{}
		if err := applyTaggingEnv(cfg); err != nil {
			t.Fatalf("applyTaggingEnv: %v", err)
		}
		if len(cfg.Tagging.Headers) != 0 {
			t.Errorf("Headers = %+v, want none", cfg.Tagging.Headers)
		}
	})
}

func TestEnvPrefixSemanticCache(t *testing.T) {
	for _, spelling := range []string{"GOMODEL_SEMANTIC_CACHE_ENABLED", "SEMANTIC_CACHE_ENABLED"} {
		t.Run(spelling, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			t.Setenv("SEMANTIC_CACHE_ENABLED", "")
			t.Setenv("GOMODEL_SEMANTIC_CACHE_ENABLED", "")
			t.Setenv("SEMANTIC_CACHE_THRESHOLD", "")
			t.Setenv("GOMODEL_SEMANTIC_CACHE_THRESHOLD", "")
			t.Setenv(spelling, "true")
			t.Setenv(strings.Replace(spelling, "ENABLED", "THRESHOLD", 1), "0.9")

			cfg := &Config{}
			if err := applyResponseSemanticEnv(&cfg.Cache.Response); err != nil {
				t.Fatalf("applySemanticEnv: %v", err)
			}
			if cfg.Cache.Response.Semantic == nil {
				t.Fatal("Semantic = nil, want enabled block")
			}
			if got := cfg.Cache.Response.Semantic.SimilarityThreshold; got != 0.9 {
				t.Errorf("SimilarityThreshold = %v, want 0.9", got)
			}
		})
	}
}
