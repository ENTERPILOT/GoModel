package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/markups"
	"github.com/enterpilot/gomodel/internal/providers"
)

// pricing.user_path_markups needs the customer_markups capability; without it
// startup fails instead of billing customers at the global rate.
func TestNew_UserPathMarkupsRequireCustomerMarkupsCapability(t *testing.T) {
	tests := []struct {
		name           string
		markupsEnabled bool
		capability     bool
		wantErr        error
	}{
		{name: "capability enabled", markupsEnabled: true, capability: true},
		{name: "capability missing", markupsEnabled: true, wantErr: markups.ErrCustomerMarkupsUnavailable},
		{name: "markups disabled ignores them", markupsEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("STORAGE_TYPE", "sqlite")
			t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "markups.db"))
			t.Setenv("ADMIN_UI_ENABLED", "false")
			t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
			t.Setenv("MCP_ENABLED", "false")
			t.Setenv("PRICING_MARKUPS_ENABLED", "")
			yaml := "pricing:\n  markups_enabled: " + strconv.FormatBool(tt.markupsEnabled) + "\n" +
				"  user_path_markups:\n    /acme:\n      \"/\": 30\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))
			loaded, err := config.Load()
			require.NoError(t, err)
			require.Len(t, loaded.Config.Pricing.UserPathMarkups, 1)

			extensions := &ext.Registry{}
			if tt.capability {
				extensions.EnableCapability(ext.CapabilityCustomerMarkups)
			}
			ctx := context.Background()
			application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory(), Extensions: extensions})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() {
				assert.NoError(t, application.Shutdown(ctx))
			})
		})
	}
}
