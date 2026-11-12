package auditlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
)

// unkeyedAPIKeyHash is the fingerprint as it was before it was keyed.
func unkeyedAPIKeyHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:APIKeyHashPrefixLength/2])
}

// The fingerprint is keyed with the deployment's secret, so a key alone does
// not give it away; without the secret there is no fingerprint at all.
func TestHashAPIKeyIsKeyed(t *testing.T) {
	const header = "Bearer test-key"

	keyed := hashAPIKey("deployment-secret", header)
	assert.Len(t, keyed, APIKeyHashPrefixLength)
	assert.NotEqual(t, unkeyedAPIKeyHash("test-key"), keyed)
	assert.NotEqual(t, hashAPIKey("another-deployment", header), keyed)
	assert.Empty(t, hashAPIKey("", header))
}

func TestMiddlewareRecordsKeyedAPIKeyHash(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "keyed with the deployment secret", secret: "deployment-secret", want: hashAPIKey("deployment-secret", "Bearer sk-test")},
		{name: "left out without a secret", secret: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &captureLiveLogger{cfg: Config{Enabled: true, APIKeyHashSecret: tt.secret}}
			c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithHeader("Authorization", "Bearer sk-test"))

			var got string
			handler := Middleware(logger)(func(c *echo.Context) error {
				entry, ok := c.Get(string(LogEntryKey)).(*LogEntry)
				require.True(t, ok)
				got = entry.Data.APIKeyHash
				return nil
			})
			require.NoError(t, handler(c))

			assert.Equal(t, tt.want, got)
			assert.NotEqual(t, unkeyedAPIKeyHash("sk-test"), got)
		})
	}
}

func newAPIKeyHashTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "audit.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// The secret is created once and kept in the database, so restarts and
// replicas sharing it give a key the same fingerprint.
func TestLoadAPIKeyHashSecretIsStablePerDatabase(t *testing.T) {
	ctx := context.Background()
	backend := newAPIKeyHashTestStorage(t)

	first := loadAPIKeyHashSecret(ctx, backend)
	require.NotEmpty(t, first)
	assert.Equal(t, first, loadAPIKeyHashSecret(ctx, backend))

	settings, err := runtimesettings.NewStore(ctx, backend)
	require.NoError(t, err)
	stored, found, err := settings.Get(ctx, apiKeyHashSecretKey)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, first, stored)

	assert.NotEqual(t, first, loadAPIKeyHashSecret(ctx, newAPIKeyHashTestStorage(t)), "another deployment has its own secret")
}

func TestNewLoadsAPIKeyHashSecret(t *testing.T) {
	ctx := context.Background()
	backend := newAPIKeyHashTestStorage(t)

	result, err := New(ctx, &config.Config{Logging: config.LogConfig{Enabled: true}}, backend)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Close() })

	assert.Equal(t, loadAPIKeyHashSecret(ctx, backend), result.Logger.Config().APIKeyHashSecret)
	assert.NotEmpty(t, result.Logger.Config().APIKeyHashSecret)
}
