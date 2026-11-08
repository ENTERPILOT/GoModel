package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// A provider client built after a rejected reload must use the serving
// generation's idle pool size, not the one the replacement asked for.
func TestNewRestoresMaxIdleConnsPerHostWhenBootstrapFails(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "pool-reload.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("HTTP_MAX_IDLE_CONNS_PER_HOST", "")
	loaded, err := config.Load()
	require.NoError(t, err)

	preTest := httpclient.ConfiguredMaxIdleConnsPerHost()
	t.Cleanup(func() { httpclient.SetConfiguredMaxIdleConnsPerHost(preTest) })
	httpclient.SetConfiguredMaxIdleConnsPerHost(64)

	// The replacement shrinks the pool and then fails a bootstrap phase.
	loaded.Config.HTTP.MaxIdleConnsPerHost = 1
	loaded.Config.Storage.Type = "not-a-backend"
	_, err = New(context.Background(), Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.Error(t, err, "expected bootstrap to fail")

	assert.Equal(t, 64, httpclient.ConfiguredMaxIdleConnsPerHost())
	assert.Equal(t, 64, httpclient.DefaultConfig().MaxIdleConnsPerHost)
}
