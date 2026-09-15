package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A configuration built in code (tests, embedders) skips config.Load, so
// the app applies the guardrails-imply-plugins rule itself.
func TestPluginsEnabled_HonoursGuardrailsWithoutLoad(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nil", nil, false},
		{"default", &config.Config{}, false},
		{"plugins", &config.Config{Plugins: config.PluginsConfig{Enabled: true}}, true},
		{"guardrails", &config.Config{Guardrails: config.GuardrailsConfig{Enabled: true}}, true},
	} {
		got := pluginsEnabled(tt.cfg)
		assert.Equal(t, tt.want, got, "%s: pluginsEnabled = %v, want %v", tt.name, got, tt.want)
	}
}

// The plugin system is off by default and takes guardrails and routing-
// strategy plugins with it; GUARDRAILS_ENABLED=true turns it back on.
func TestNew_PluginSystemFlag(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"disabled by default", nil, false},
		{"plugins enabled", map[string]string{"PLUGINS_ENABLED": "true"}, true},
		{"guardrails imply plugins", map[string]string{"GUARDRAILS_ENABLED": "true"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("STORAGE_TYPE", "sqlite")
			t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "plugins.db"))
			t.Setenv("ADMIN_UI_ENABLED", "false")
			t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
			t.Setenv("MCP_ENABLED", "false")
			t.Setenv("PLUGINS_ENABLED", "")
			t.Setenv("GUARDRAILS_ENABLED", "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			loaded, err := config.Load()
			require.NoError(t, err)

			ctx := context.Background()
			app, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
			require.NoError(t, err)

			defer func() {
				err := app.Shutdown(ctx)
				assert.NoError(t, err)

			}()
			got := app.pluginCatalog != nil
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want, app.routeStrategies != nil)
			require.Equal(t, tt.want, app.guardrails != nil, "catalog=%v resolver=%v guardrails=%v, want all %v", app.pluginCatalog != nil, app.routeStrategies != nil, app.guardrails != nil, tt.want)

			if tt.want && app.guardrails.Service == nil {
				t.Fatal("guardrails service missing with the plugin system on")
			}
		})
	}
}
