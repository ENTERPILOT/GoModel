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
