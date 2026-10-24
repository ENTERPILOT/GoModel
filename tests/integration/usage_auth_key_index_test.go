//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The auth_key_id index is added to usage tables that already hold history,
// so it is built in the background; it must end up valid whether or not
// usage tracking is enabled.
func TestUsage_AuthKeyIndexBuilds_PostgreSQL(t *testing.T) {
	for _, tc := range []struct {
		name         string
		usageEnabled bool
	}{
		{name: "usage enabled", usageEnabled: true},
		{name: "usage disabled", usageEnabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := GetPostgreSQLPool()
			_, err := pool.Exec(ctx, `DROP INDEX IF EXISTS idx_usage_auth_key_id`)
			require.NoError(t, err)

			SetupTestServer(t, TestServerConfig{
				DBType:       "postgresql",
				UsageEnabled: tc.usageEnabled,
			})

			require.Eventually(t, func() bool {
				var valid bool
				err := pool.QueryRow(ctx,
					`SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass('idx_usage_auth_key_id')`,
				).Scan(&valid)
				return err == nil && valid
			}, 30*time.Second, 100*time.Millisecond, "auth key index never became valid")
		})
	}
}
