package config

import (
	"testing"
)

func TestLoad_PluginsSection(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("PLUGINS_SEARCH_PATHS", "")
	withTempDir(t, func(dir string) {
		writeConfigYAML(t, dir, `
plugins:
  search_paths: ["/etc/gomodel/plugins", "./plugins"]
  load:
    - file: keyword_block.so
      sha256: "abc"
    - file: /opt/acme/guard.so
`)
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		p := result.Config.Plugins
		if len(p.SearchPaths) != 2 || p.SearchPaths[0] != "/etc/gomodel/plugins" || p.SearchPaths[1] != "./plugins" {
			t.Fatalf("SearchPaths = %v", p.SearchPaths)
		}
		if len(p.Load) != 2 || p.Load[0].File != "keyword_block.so" || p.Load[0].SHA256 != "abc" || p.Load[1].File != "/opt/acme/guard.so" || p.Load[1].SHA256 != "" {
			t.Fatalf("Load = %+v", p.Load)
		}
	})
}

func TestLoad_PluginsDefaultsAndEnv(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("PLUGINS_SEARCH_PATHS", "")
	withTempDir(t, func(string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if len(result.Config.Plugins.SearchPaths) != 0 || len(result.Config.Plugins.Load) != 0 {
			t.Fatalf("default Plugins = %+v, want empty", result.Config.Plugins)
		}
	})

	t.Setenv("PLUGINS_SEARCH_PATHS", "/a, /b ,")
	withTempDir(t, func(string) {
		result, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		got := result.Config.Plugins.SearchPaths
		if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
			t.Fatalf("SearchPaths from env = %v, want [/a /b]", got)
		}
	})
}

func TestLoad_PluginsEnabledFlag(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		yaml string
		want bool
	}{
		{"disabled by default", nil, "", false},
		{"env enables", map[string]string{"PLUGINS_ENABLED": "true"}, "", true},
		{"yaml enables", nil, "plugins:\n  enabled: true\n", true},
		{"guardrails imply plugins", map[string]string{"GUARDRAILS_ENABLED": "true"}, "", true},
		{"guardrails imply plugins over yaml", map[string]string{"GUARDRAILS_ENABLED": "true"}, "plugins:\n  enabled: false\n", true},
		{"load without enabled stays off", nil, "plugins:\n  load:\n    - file: a.so\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAllConfigEnvVars(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			withTempDir(t, func(dir string) {
				if tt.yaml != "" {
					writeConfigYAML(t, dir, tt.yaml)
				}
				result, err := Load()
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if got := result.Config.Plugins.Enabled; got != tt.want {
					t.Fatalf("Plugins.Enabled = %v, want %v", got, tt.want)
				}
			})
		})
	}
}
