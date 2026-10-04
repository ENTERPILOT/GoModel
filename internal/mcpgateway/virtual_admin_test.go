package mcpgateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// newVirtualAdminTestService serves alpha and beta upstreams from config,
// with in-memory stores for servers and virtual servers.
func newVirtualAdminTestService(t *testing.T, configVirtuals map[string]VirtualServerSpec, stored ...ManagedVirtualServer) (*Service, *memoryVirtualStore, string) {
	t.Helper()
	virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{}}
	for _, row := range stored {
		virtualStore.rows[row.Name] = row
	}
	service, gatewayURL := newTestServiceWithOptions(t, Options{
		ConfigServers: map[string]ServerSpec{
			"alpha": testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil),
			"beta":  testSpec("beta", newTestUpstream(t, "beta", addEchoTool("search")), nil),
		},
		Store:          &memoryStore{rows: map[string]ManagedServer{}},
		VirtualServers: configVirtuals,
		VirtualStore:   virtualStore,
	})
	return service, virtualStore, gatewayURL
}

func TestUpsertVirtualServesImmediately(t *testing.T) {
	service, virtualStore, gatewayURL := newVirtualAdminTestService(t, nil)

	view, err := service.UpsertVirtual(context.Background(), ManagedVirtualServer{
		Name:          " Coding ",
		Description:   "code tools",
		Servers:       []string{"beta", "alpha"},
		ToolDiscovery: config.MCPToolDiscoverySearch,
	})
	require.NoError(t, err)
	require.Contains(t, virtualStore.rows, "coding", "the name is normalized before it is stored")
	assert.Equal(t, "coding", view.Spec.Name, "the saved definition is returned")
	assert.Equal(t, config.MCPToolDiscoverySearch, view.ToolDiscovery)

	views := service.VirtualViews()
	require.Len(t, views, 1)
	assert.False(t, views[0].Spec.Managed)
	assert.Equal(t, []string{"beta", "alpha"}, views[0].Spec.Servers)

	session := connectClient(t, gatewayURL+"/mcp/coding", map[string]string{ToolDiscoveryHeader: config.MCPToolDiscoveryOff})
	assert.Equal(t, []string{"alpha_echo", "beta_search"}, listToolNames(t, session))
}

func TestUpsertVirtualRejections(t *testing.T) {
	tests := []struct {
		name    string
		virtual ManagedVirtualServer
		wantErr string
	}{
		{
			name:    "unknown member",
			virtual: ManagedVirtualServer{Name: "coding", Servers: []string{"alpha", "ghost"}},
			wantErr: `member "ghost" matches no MCP server`,
		},
		{
			name:    "nested virtual server",
			virtual: ManagedVirtualServer{Name: "all", Servers: []string{"research"}},
			wantErr: `member "research" is a virtual server; virtual servers can only include MCP servers`,
		},
		{
			name:    "server slug",
			virtual: ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}},
			wantErr: `name "alpha" is used by MCP server "alpha"; choose another name`,
		},
		{
			name:    "config-declared name",
			virtual: ManagedVirtualServer{Name: "research", Servers: []string{"beta"}},
			wantErr: `virtual MCP server "research" is managed by config/env and is read-only`,
		},
		{
			name:    "invalid name",
			virtual: ManagedVirtualServer{Name: "my tools", Servers: []string{"alpha"}},
			wantErr: `virtual server name "my tools" must match`,
		},
		{
			name:    "no members",
			virtual: ManagedVirtualServer{Name: "coding"},
			wantErr: "servers is required",
		},
		{
			name:    "invalid tool discovery",
			virtual: ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}, ToolDiscovery: "semantic"},
			wantErr: `tool_discovery must be "off" or "search"`,
		},
	}
	service, virtualStore, _ := newVirtualAdminTestService(t, map[string]VirtualServerSpec{
		"research": {Name: "research", Servers: []string{"beta"}, Managed: true},
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.UpsertVirtual(context.Background(), tt.virtual)
			require.ErrorIs(t, err, ErrInvalidVirtualServer)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, virtualStore.rows)
		})
	}
}

func TestUpsertVirtualKeepsRowStoredBeforeServerEditable(t *testing.T) {
	// The row predates the "alpha" server, so it is not served but stays editable.
	service, virtualStore, _ := newVirtualAdminTestService(t, nil,
		ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}})
	require.NotEmpty(t, service.VirtualViews()[0].Conflict)

	_, err := service.UpsertVirtual(context.Background(), ManagedVirtualServer{Name: "alpha", Description: "renamed soon", Servers: []string{"beta"}})
	require.NoError(t, err)
	assert.Equal(t, "renamed soon", virtualStore.rows["alpha"].Description)

	// If the row is deleted while the edit is in flight, the edit must not
	// recreate it under the server's slug.
	delete(virtualStore.rows, "alpha")
	_, err = service.UpsertVirtual(context.Background(), ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}})
	require.ErrorIs(t, err, ErrInvalidVirtualServer)
	assert.Empty(t, virtualStore.rows)
}

func TestConfigVirtualServerShadowsStoredRow(t *testing.T) {
	service, _, _ := newVirtualAdminTestService(t,
		map[string]VirtualServerSpec{"coding": {Name: "coding", Servers: []string{"alpha"}, Managed: true}},
		ManagedVirtualServer{Name: "coding", Servers: []string{"beta"}},
	)

	views := service.VirtualViews()
	require.Len(t, views, 1)
	assert.True(t, views[0].Spec.Managed)
	assert.Equal(t, []string{"alpha"}, views[0].Spec.Servers)
	assert.True(t, service.IsManagedVirtual("coding"))
}

func TestDeleteVirtualEndsItsEndpoint(t *testing.T) {
	service, virtualStore, gatewayURL := newVirtualAdminTestService(t,
		map[string]VirtualServerSpec{"research": {Name: "research", Servers: []string{"beta"}, Managed: true}},
		ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}},
	)
	sessionID := initializeRawSession(t, gatewayURL+"/mcp/coding", nil)
	service.bindMu.Lock()
	host := service.bindings[sessionID].server
	service.bindMu.Unlock()
	require.NotNil(t, host)
	require.NotEmpty(t, slices.Collect(host.Sessions()))

	require.NoError(t, service.DeleteVirtual(context.Background(), " Coding "), "names are normalized like on save")
	assert.Empty(t, virtualStore.rows)
	assert.False(t, service.IsVirtual("coding"))

	// The session is closed and unbound now, not at the idle timeout.
	service.bindMu.Lock()
	_, bound := service.bindings[sessionID]
	service.bindMu.Unlock()
	assert.False(t, bound)
	assert.Eventually(t, func() bool { return len(slices.Collect(host.Sessions())) == 0 }, 5*time.Second, 10*time.Millisecond)

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := rawMCPStatus(t, gatewayURL+"/mcp/coding", listBody, map[string]string{"Mcp-Session-Id": sessionID})
	assert.Equal(t, http.StatusNotFound, status, "a session on a deleted virtual server ends")

	require.ErrorIs(t, service.DeleteVirtual(context.Background(), "coding"), ErrNotFound)
	err := service.DeleteVirtual(context.Background(), "Research")
	require.ErrorIs(t, err, ErrInvalidVirtualServer, "config-declared virtual servers are read-only, whatever the case")
}

func TestUpsertServerRejectsStoredVirtualName(t *testing.T) {
	service, _, _ := newVirtualAdminTestService(t, nil,
		ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}})

	err := service.Upsert(context.Background(), ManagedServer{Name: "coding", URL: "https://example.com/mcp", Transport: config.MCPTransportHTTP})
	require.ErrorIs(t, err, ErrVirtualNameTaken)
	assert.Equal(t, `slug "coding" is used by virtual MCP server "coding"; choose another slug`, err.Error())
}

func TestVirtualMemberRemovalReachesOpenSessions(t *testing.T) {
	service, _, gatewayURL := newVirtualAdminTestService(t, nil,
		ManagedVirtualServer{Name: "coding", Servers: []string{"alpha", "beta"}})
	session := connectClient(t, gatewayURL+"/mcp/coding", nil)
	require.Equal(t, []string{"alpha_echo", "beta_search"}, listToolNames(t, session))

	_, err := service.UpsertVirtual(context.Background(), ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}})
	require.NoError(t, err)

	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "beta_search"})
	require.Error(t, err, "a removed member must not stay callable from an open session")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_echo"})
	require.NoError(t, err)
	assert.False(t, result.IsError, "remaining members keep working")
}

func TestBackgroundRefreshPicksUpOtherInstanceWrites(t *testing.T) {
	virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{}}
	service, _ := newTestServiceWithOptions(t, Options{
		ConfigServers:   map[string]ServerSpec{"alpha": testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil)},
		Store:           &memoryStore{rows: map[string]ManagedServer{}},
		VirtualStore:    virtualStore,
		RefreshInterval: 20 * time.Millisecond,
	})

	// Another gateway instance sharing the database saves a virtual server.
	virtualStore.put(ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}})
	require.Eventually(t, func() bool { return service.IsVirtual("coding") }, 5*time.Second, 10*time.Millisecond)
}

// gatedStore blocks List while armed, standing in for a slow store read that
// a reload is in the middle of when Close runs. honorCtx makes the read
// return on cancellation, like a real database driver.
type gatedStore struct {
	*memoryStore
	gate     chan struct{}
	entered  chan struct{}
	honorCtx bool
}

func (g *gatedStore) List(ctx context.Context) ([]ManagedServer, error) {
	if g.gate != nil {
		g.entered <- struct{}{}
		if g.honorCtx {
			select {
			case <-g.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			<-g.gate
		}
	}
	return g.memoryStore.List(ctx)
}

func TestCloseWaitsForInFlightReloadAndStopsLaterOnes(t *testing.T) {
	lateURL := newTestUpstream(t, "late", addEchoTool("echo"))
	store := &gatedStore{memoryStore: &memoryStore{rows: map[string]ManagedServer{}}}
	service, err := NewService(context.Background(), Options{Store: store})
	require.NoError(t, err)
	t.Cleanup(service.Close)

	// A server saved on another instance, read by a refresh that is slow.
	store.rows["late"] = ManagedServer{Name: "late", URL: lateURL, Transport: config.MCPTransportHTTP, Enabled: true}
	store.gate, store.entered = make(chan struct{}), make(chan struct{}, 1)
	reloaded := make(chan error, 1)
	go func() { reloaded <- service.Reload(context.Background()) }()
	<-store.entered

	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a reload was still applying")
	case <-time.After(100 * time.Millisecond):
	}

	close(store.gate)
	<-closed
	require.NoError(t, <-reloaded)

	late, ok := service.manager.get("late")
	require.True(t, ok, "the in-flight reload applied before Close")
	late.stateMu.Lock()
	lateClosed := late.closed
	late.stateMu.Unlock()
	assert.True(t, lateClosed, "an upstream added by the in-flight reload is closed by Close")

	store.gate = nil
	require.ErrorIs(t, service.Reload(context.Background()), errServiceClosed, "reloads after Close do nothing")
}

func TestCloseCancelsAdminReloadBlockedOnStore(t *testing.T) {
	store := &gatedStore{memoryStore: &memoryStore{rows: map[string]ManagedServer{}}, honorCtx: true}
	service, err := NewService(context.Background(), Options{Store: store})
	require.NoError(t, err)

	// An admin save's reload, with a request context that never ends, waits
	// on a slow store read.
	store.gate, store.entered = make(chan struct{}), make(chan struct{}, 1)
	reloaded := make(chan error, 1)
	go func() { reloaded <- service.Reload(context.Background()) }()
	<-store.entered

	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on an admin reload blocked on the store")
	}
	require.Error(t, <-reloaded, "the blocked reload is cancelled")
}

func TestManagerCloseCancelsInFlightConnect(t *testing.T) {
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// An upstream that never answers its first request, so the connect hangs.
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server notices a client hang-up only once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case <-cancelled:
			default:
				close(cancelled)
			}
		case <-release:
		}
	}))
	t.Cleanup(hanging.Close)

	manager := NewManager(http.DefaultClient)
	manager.Apply([]ServerSpec{testSpec("slow", hanging.URL, nil)})
	<-entered

	manager.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the in-flight upstream connect")
	}
}

func TestCancelledDialEndsConnectToUnresponsiveUpstream(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{}, 1)
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hanging.Close)

	u := newUpstream(testSpec("slow", hanging.URL, nil), http.DefaultClient)
	t.Cleanup(u.close)
	ctx, cancel := context.WithCancel(context.Background())
	refreshed := make(chan error, 1)
	go func() { refreshed <- u.refresh(ctx) }()
	<-entered

	// The SDK waits for in-flight requests when a handshake fails, so this
	// returns only because the dial's HTTP requests are cancelled with it.
	cancel()
	select {
	case err := <-refreshed:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled dial kept waiting on an unresponsive upstream")
	}
}
