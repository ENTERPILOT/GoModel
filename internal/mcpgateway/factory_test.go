package mcpgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

	result, err := New(ctx, &config.Config{}, shared, box, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Close() })

	assert.NotNil(t, result.Service.virtualStore, "virtual servers keep their store when headers are sealed")
	_, err = result.Service.UpsertVirtual(ctx, ManagedVirtualServer{Name: "coding", Servers: []string{"ghost"}})
	require.ErrorIs(t, err, ErrInvalidVirtualServer, "the store is reachable: validation, not missing persistence, rejects this")
}

func TestConfigServerSpecsDeclaredServerWinsSlugClash(t *testing.T) {
	declared := map[string]config.MCPServerConfig{
		"zai-web-search": {URL: "https://declared.example.com/mcp", Transport: config.MCPTransportHTTP},
	}
	providerServers := map[string]config.MCPServerConfig{
		"zai-web-search": {URL: "https://api.z.ai/api/mcp/web_search_prime/mcp", Transport: config.MCPTransportHTTP},
		"zai-web-reader": {URL: "https://api.z.ai/api/mcp/web_reader/mcp", Transport: config.MCPTransportHTTP, Headers: map[string]string{"Authorization": "Bearer k"}},
	}

	specs := configServerSpecs(declared, providerServers)

	require.Len(t, specs, 2)
	assert.Equal(t, "https://declared.example.com/mcp", specs["zai-web-search"].URL)
	reader := specs["zai-web-reader"]
	assert.Equal(t, "https://api.z.ai/api/mcp/web_reader/mcp", reader.URL)
	assert.Equal(t, "Bearer k", reader.Headers["Authorization"])
	assert.True(t, reader.Managed, "provider-hosted servers are read-only in the dashboard")
}

// Provider-hosted servers are config servers like any other, so their
// sessions share across replicas through the same store: New wires both.
func TestProviderServerSessionsContinueOnAnotherReplica(t *testing.T) {
	ctx := context.Background()
	providerServers := map[string]config.MCPServerConfig{
		"zai-web-search": {URL: newTestUpstream(t, "zai", addEchoTool("search")), Transport: config.MCPTransportHTTP},
	}
	sessions := newMemorySharedStore(time.Now)
	replica := func() string {
		shared, err := storage.New(ctx, storage.Config{
			Type:   storage.TypeSQLite,
			SQLite: storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "gomodel.db")},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = shared.Close() })
		result, err := New(ctx, &config.Config{}, shared, nil, nil, nil, nil, providerServers, sessions)
		require.NoError(t, err)
		t.Cleanup(func() { _ = result.Close() })
		gateway := httptest.NewServer(gatewayHandler(result.Service))
		t.Cleanup(gateway.Close)
		waitForConnected(t, result.Service, 1)
		return gateway.URL
	}
	a, b := replica(), replica()

	sessionID := initializeRawSession(t, a+"/mcp/zai-web-search", nil)
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, b+"/mcp/zai-web-search", listToolsBody, withSession(nil, sessionID)))
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, b+"/mcp", listToolsBody, withSession(nil, sessionID)), "the session stays bound to its endpoint")
}
