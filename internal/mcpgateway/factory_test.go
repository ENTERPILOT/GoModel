package mcpgateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Regression: with encryption on, the server store is wrapped by a sealing
// layer that implements only Store, so taking the virtual store from it left
// dashboard virtual servers without persistence.
func TestNewKeepsVirtualServerPersistenceWithEncryption(t *testing.T) {
	ctx := context.Background()
	shared, err := storage.New(ctx, storage.Config{
		Type:   storage.TypeSQLite,
		SQLite: storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "gomodel.db")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })
	box, _ := encryptiontest.NewBox(t)

	result, err := New(ctx, &config.Config{}, shared, box, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Close() })

	assert.NotNil(t, result.Service.virtualStore, "virtual servers keep their store when headers are sealed")
	_, err = result.Service.UpsertVirtual(ctx, ManagedVirtualServer{Name: "coding", Servers: []string{"ghost"}})
	require.ErrorIs(t, err, ErrInvalidVirtualServer, "the store is reachable: validation, not missing persistence, rejects this")
}
