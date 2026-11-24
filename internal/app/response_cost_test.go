package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/server"
)

// A request without a usage row has no cost to return, so startup turns
// response costs off when usage tracking is off.
func TestNew_ResponseCostNeedsUsageTracking(t *testing.T) {
	tests := []struct {
		name         string
		usageEnabled string
		want         bool
	}{
		{name: "usage tracking on", usageEnabled: "true", want: true},
		{name: "usage tracking off", usageEnabled: "false", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("STORAGE_TYPE", "sqlite")
			t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "costs.db"))
			t.Setenv("ADMIN_UI_ENABLED", "false")
			t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
			t.Setenv("MCP_ENABLED", "false")
			t.Setenv("USAGE_ENABLED", tt.usageEnabled)
			t.Setenv("USAGE_RESPONSE_COST_ENABLED", "true")
			loaded, err := config.Load()
			require.NoError(t, err)
			require.True(t, loaded.Config.Usage.ResponseCostEnabled)

			ctx := context.Background()
			application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, application.Shutdown(ctx)) })

			assert.Equal(t, tt.want, application.config.Usage.ResponseCostEnabled)
		})
	}
}

// The billed cost is returned exactly when markups are enabled.
func TestApplyResponseCost(t *testing.T) {
	tests := []struct {
		name           string
		enabled        bool
		markupsEnabled bool
		wantBilled     bool
	}{
		{name: "provider cost", enabled: true},
		{name: "billed cost", enabled: true, markupsEnabled: true, wantBilled: true},
		{name: "off", markupsEnabled: true, wantBilled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCfg := &config.Config{
				Usage:   config.UsageConfig{ResponseCostEnabled: tt.enabled},
				Pricing: config.PricingConfig{MarkupsEnabled: tt.markupsEnabled},
			}
			serverCfg := &server.Config{}

			applyResponseCost(serverCfg, appCfg)

			assert.Equal(t, tt.enabled, serverCfg.ResponseCostEnabled)
			assert.Equal(t, tt.wantBilled, serverCfg.ResponseCostBilled)
		})
	}
}
