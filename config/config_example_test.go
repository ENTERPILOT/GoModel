package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_FromEnvironment(t *testing.T) {
	_ = os.Setenv("PORT", "9090")
	defer func() {
		_ = os.Unsetenv("PORT")
	}()

	result, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Config.Server.Port != "9090" {
		t.Errorf("expected port 9090, got %s", result.Config.Server.Port)
	}
}

// TestLoad_ConfigExample_IncludesKimiProvider verifies that the example
// configuration registers the Kimi provider block (added in Task 1.6) and that
// the new `custom_upstream_headers` YAML key parses without errors. The test
// loads the example config via Load() and asserts the parsed RawProviders map
// contains a Kimi entry with the expected type and that a representative YAML
// snippet using `custom_upstream_headers` round-trips into
// RawProviderConfig.CustomUpstreamHeaders.
func TestLoad_ConfigExample_IncludesKimiProvider(t *testing.T) {
	clearAllConfigEnvVars(t)

	examplePath, err := filepath.Abs("config.example.yaml")
	if err != nil {
		t.Fatalf("failed to resolve config.example.yaml path: %v", err)
	}
	exampleData, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("failed to read config.example.yaml: %v", err)
	}
	failoverExamplePath, err := filepath.Abs("failover.example.json")
	if err != nil {
		t.Fatalf("failed to resolve failover.example.json path: %v", err)
	}
	failoverExampleData, err := os.ReadFile(failoverExamplePath)
	if err != nil {
		t.Fatalf("failed to read failover.example.json: %v", err)
	}

	withTempDir(t, func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, "config"), 0755); err != nil {
			t.Fatalf("failed to create config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.yaml"), exampleData, 0644); err != nil {
			t.Fatalf("failed to write config/config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "failover.example.json"), failoverExampleData, 0644); err != nil {
			t.Fatalf("failed to write failover.example.json: %v", err)
		}

		result, err := Load()
		if err != nil {
			t.Fatalf("Load() of config.example.yaml failed: %v", err)
		}

		kimi, exists := result.RawProviders["kimi"]
		if !exists {
			t.Fatalf("expected RawProviders to contain a kimi entry; got keys: %v", keysOf(result.RawProviders))
		}
		if kimi.Type != "kimi" {
			t.Fatalf("expected kimi provider type %q, got %q", "kimi", kimi.Type)
		}
		if kimi.APIKey != "${KIMI_API_KEY}" {
			t.Fatalf("expected kimi api_key %q, got %q", "${KIMI_API_KEY}", kimi.APIKey)
		}

		// Verify `custom_upstream_headers` parses from a representative snippet.
		withTempDir(t, func(dir2 string) {
			yaml := `
providers:
  kimi:
    type: kimi
    api_key: "test-key"
    custom_upstream_headers:
      User-Agent: "MyApp/1.0"
      X-Title: "MyApp"
      X-Stainless-Os: "Linux"
`
			if err := os.WriteFile(filepath.Join(dir2, "config.yaml"), []byte(yaml), 0644); err != nil {
				t.Fatalf("failed to write config.yaml: %v", err)
			}
			res, err := Load()
			if err != nil {
				t.Fatalf("Load() of custom_upstream_headers snippet failed: %v", err)
			}
			cfg, exists := res.RawProviders["kimi"]
			if !exists {
				t.Fatalf("expected RawProviders to contain kimi from snippet")
			}
			want := map[string]string{
				"User-Agent":   "MyApp/1.0",
				"X-Title":      "MyApp",
				"X-Stainless-Os": "Linux",
			}
			if !reflect.DeepEqual(cfg.CustomUpstreamHeaders, want) {
				t.Fatalf("CustomUpstreamHeaders = %v, want %v", cfg.CustomUpstreamHeaders, want)
			}
		})
	})
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
