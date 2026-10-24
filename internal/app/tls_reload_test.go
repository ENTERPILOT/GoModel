package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// A reload whose replacement is rejected must not leave that replacement's
// outbound trust installed for the generation that keeps serving.
func TestNewRestoresTLSWhenBootstrapFails(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "tls-reload.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	loaded, err := config.Load()
	require.NoError(t, err)

	// The serving generation trusts nothing extra but skips verification;
	// distinctive enough to detect whether it survives a failed reload.
	preTest := httpclient.SnapshotTLS()
	t.Cleanup(func() { httpclient.RestoreTLS(preTest) })
	require.NoError(t, httpclient.SetConfiguredTLS(httpclient.TLSSettings{InsecureSkipVerify: true}))

	// The replacement asks for system defaults and then fails a bootstrap
	// phase: an unusable storage backend trips the first one.
	loaded.Config.Storage.Type = "not-a-backend"
	_, err = New(context.Background(), Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.Error(t, err, "expected bootstrap to fail")
	got := httpclient.ConfiguredTLS()
	require.NotNil(t, got, "rejected replacement leaked: serving generation's TLS configuration was not restored")
	require.True(t, got.InsecureSkipVerify, "rejected replacement leaked: serving generation's TLS configuration was not restored")
}
