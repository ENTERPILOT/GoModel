package config

import (
	"testing"

	"github.com/stretchr/testify/require"
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
		require.NoError(t, err)

		p := result.Config.Plugins
		require.Len(t, p.SearchPaths, 2)
		require.Equal(t, "/etc/gomodel/plugins", p.SearchPaths[0])
		require.Equal(t, "./plugins", p.SearchPaths[1])
		require.Len(t, p.Load, 2)
		require.Equal(t, "keyword_block.so", p.Load[0].File)
		require.Equal(t, "abc", p.Load[0].SHA256)
		require.Equal(t, "/opt/acme/guard.so", p.Load[1].File)
		require.Empty(t, p.Load[1].SHA256)
	})
}

func TestLoad_PluginsDefaultsAndEnv(t *testing.T) {
	clearAllConfigEnvVars(t)
	t.Setenv("PLUGINS_SEARCH_PATHS", "")
	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.Empty(t, result.Config.Plugins.SearchPaths)
		require.Empty(t, result.Config.Plugins.Load, "default Plugins = %+v, want empty", result.Config.Plugins)
	})

	t.Setenv("PLUGINS_SEARCH_PATHS", "/a, /b ,")
	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)

		got := result.Config.Plugins.SearchPaths
		require.Len(t, got, 2)
		require.Equal(t, "/a", got[0])
		require.Equal(t, "/b", got[1])
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
				require.NoError(t, err)
				got := result.Config.Plugins.Enabled
				require.Equal(t, tt.want, got)
			})
		})
	}
}
