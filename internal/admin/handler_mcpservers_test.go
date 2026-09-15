package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// mcpAdminFake is an in-memory MCPServerAdmin for handler tests: it stands in
// for *mcpgateway.Service without dialing any upstream.
type mcpAdminFake struct {
	views        map[string]mcpgateway.ServerView
	managed      map[string]struct{}
	stored       map[string]mcpgateway.ManagedServer
	catalogs     map[string]mcpgateway.CatalogView
	upsertErr    error
	deleteErr    error
	reconnectErr error
}

func newMCPAdminFake() *mcpAdminFake {
	return &mcpAdminFake{
		views:   map[string]mcpgateway.ServerView{},
		managed: map[string]struct{}{},
		stored:  map[string]mcpgateway.ManagedServer{},
	}
}

// addManaged registers a config-declared (read-only) server view.
func (f *mcpAdminFake) addManaged(view mcpgateway.ServerView) {
	view.Spec.Managed = true
	f.views[view.Spec.Name] = view
	f.managed[view.Spec.Name] = struct{}{}
}

// addStored registers an admin-store row and its runtime view.
func (f *mcpAdminFake) addStored(server mcpgateway.ManagedServer, status mcpgateway.ServerStatus) {
	f.stored[server.Name] = server
	f.views[server.Name] = mcpgateway.ServerView{Spec: server.Spec(), Status: status}
}

func (f *mcpAdminFake) Views() []mcpgateway.ServerView {
	names := make([]string, 0, len(f.views))
	for name := range f.views {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]mcpgateway.ServerView, 0, len(names))
	for _, name := range names {
		views = append(views, f.views[name])
	}
	return views
}

func (f *mcpAdminFake) IsManaged(name string) bool {
	_, ok := f.managed[name]
	return ok
}

func (f *mcpAdminFake) GetManaged(_ context.Context, name string) (*mcpgateway.ManagedServer, error) {
	row, ok := f.stored[name]
	if !ok {
		return nil, mcpgateway.ErrNotFound
	}
	clone := row
	return &clone, nil
}

func (f *mcpAdminFake) Upsert(_ context.Context, server mcpgateway.ManagedServer) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.addStored(server, mcpgateway.StatusConnecting)
	return nil
}

func (f *mcpAdminFake) Delete(_ context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.stored[name]; !ok {
		return mcpgateway.ErrNotFound
	}
	delete(f.stored, name)
	delete(f.views, name)
	return nil
}

func (f *mcpAdminFake) Reconnect(_ context.Context, name string) (mcpgateway.ServerView, error) {
	view, ok := f.views[name]
	if !ok {
		return mcpgateway.ServerView{}, fmt.Errorf("mcp server %q is not configured", name)
	}
	if f.reconnectErr != nil {
		view.Status = mcpgateway.StatusDegraded
		view.LastError = f.reconnectErr.Error()
		f.views[name] = view
		return view, f.reconnectErr
	}
	view.Status = mcpgateway.StatusConnected
	f.views[name] = view
	return view, nil
}

func (f *mcpAdminFake) Catalog(name string) (mcpgateway.CatalogView, bool) {
	view, ok := f.views[name]
	if !ok {
		return mcpgateway.CatalogView{}, false
	}
	catalog := mcpgateway.CatalogView{
		Server:    name,
		Status:    view.Status,
		Tools:     []mcpgateway.CatalogFeature{},
		Prompts:   []mcpgateway.CatalogFeature{},
		Resources: []mcpgateway.CatalogResource{},
		Templates: []mcpgateway.CatalogTemplate{},
	}
	if stored, ok := f.catalogs[name]; ok {
		catalog = stored
		catalog.Server = name
		catalog.Status = view.Status
	}
	return catalog, true
}

func newMCPHandler(fake *mcpAdminFake) *Handler {
	return NewHandler(nil, nil, WithMCPServers(fake))
}

func TestListMCPServers_RedactsHeadersAndFlagsManaged(t *testing.T) {
	fake := newMCPAdminFake()
	connectedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	fake.addManaged(mcpgateway.ServerView{
		Spec: mcpgateway.ServerSpec{
			Name:        "github",
			URL:         "https://api.githubcopilot.com/mcp/",
			Transport:   "http",
			Headers:     map[string]string{"Authorization": "Bearer top-secret"},
			Enabled:     true,
			ToolTimeout: 30 * time.Second,
		},
		Status:      mcpgateway.StatusConnected,
		ToolCount:   3,
		ConnectedAt: connectedAt,
	})
	fake.addStored(mcpgateway.ManagedServer{
		Name:    "notion",
		URL:     "https://mcp.notion.com/mcp",
		Headers: map[string]string{"X-Api-Key": "sk-hidden"},
		Enabled: true,
	}, mcpgateway.StatusConnecting)
	h := newMCPHandler(fake)

	c, rec := echotest.Get(t, "/admin/mcp-servers")
	err := h.ListMCPServers(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, secret := range []string{"top-secret", "sk-hidden"} {
		assert.NotContains(t, rec.Body.String(), secret)
	}

	body := echotest.Decode[[]mcpServerViewResponse](t, rec)
	require.Len(t, body, 2)

	byName := map[string]mcpServerViewResponse{}
	for _, view := range body {
		byName[view.Name] = view
	}

	github := byName["github"]
	assert.True(t, github.Managed)
	assert.Equal(t, "***", github.Headers["Authorization"])
	assert.Equal(t, string(mcpgateway.StatusConnected), github.Status)
	assert.Equal(t, 3, github.ToolCount)
	require.NotNil(t, github.ConnectedAt)
	assert.True(t, github.ConnectedAt.Equal(connectedAt))

	notion := byName["notion"]
	assert.False(t, notion.Managed)
	assert.Equal(t, "***", notion.Headers["X-Api-Key"])
	assert.Nil(t, notion.ConnectedAt)
}

func TestMCPServerEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		require.NoError(t, err)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
		body := echotest.Decode[map[string]map[string]any](t, rec)
		assert.Equal(t, "feature_unavailable", body["error"]["code"], name)
	}

	listCtx, listRec := echotest.Get(t, "/admin/mcp-servers")
	assertUnavailable("ListMCPServers", h.ListMCPServers(listCtx), listRec)

	putCtx, putRec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", `{"name":"notion","url":"https://mcp.notion.com/mcp"}`)
	assertUnavailable("UpsertMCPServer", h.UpsertMCPServer(putCtx), putRec)

	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/mcp-servers/notion", nil, echotest.WithPathValue("name", "notion"))
	assertUnavailable("DeleteMCPServer", h.DeleteMCPServer(deleteCtx), deleteRec)

	reconnectCtx, reconnectRec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/reconnect", nil, echotest.WithPathValue("name", "notion"))
	assertUnavailable("ReconnectMCPServer", h.ReconnectMCPServer(reconnectCtx), reconnectRec)
}

func TestUpsertMCPServer_CreatesAndReturnsRedactedView(t *testing.T) {
	fake := newMCPAdminFake()
	h := newMCPHandler(fake)

	body := `{"name":"notion","url":"https://mcp.notion.com/mcp","headers":{"Authorization":"Bearer real-token"},"description":"notes","user_paths":["/team"]}`
	c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", body)
	err := h.UpsertMCPServer(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "real-token")

	view := echotest.Decode[mcpServerViewResponse](t, rec)
	assert.Equal(t, "notion", view.Name)
	assert.Equal(t, "https://mcp.notion.com/mcp", view.URL)
	assert.Equal(t, "http", view.Transport)
	assert.True(t, view.Enabled)
	assert.Equal(t, "***", view.Headers["Authorization"])

	stored, ok := fake.stored["notion"]
	require.True(t, ok, "upsert did not reach the service")
	assert.Equal(t, "Bearer real-token", stored.Headers["Authorization"])
}

func TestUpsertMCPServer_PreservesRedactedHeadersAndEnabled(t *testing.T) {
	fake := newMCPAdminFake()
	fake.addStored(mcpgateway.ManagedServer{
		Name:      "notion",
		URL:       "https://mcp.notion.com/mcp",
		Transport: "http",
		Headers:   map[string]string{"Authorization": "Bearer original", "X-Region": "eu"},
		Enabled:   false,
	}, mcpgateway.StatusDisabled)
	h := newMCPHandler(fake)

	// The dashboard round-trips the redacted view: "***" keeps the stored
	// secret, plain values overwrite, and the omitted enabled flag is preserved.
	body := `{"name":"notion","url":"https://mcp.notion.com/mcp","headers":{"Authorization":"***","X-Region":"us"}}`
	c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", body)
	err := h.UpsertMCPServer(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored := fake.stored["notion"]
	assert.Equal(t, "Bearer original", stored.Headers["Authorization"])
	assert.Equal(t, "us", stored.Headers["X-Region"])
	assert.False(t, stored.Enabled)
}

func TestUpsertMCPServer_AllowsUnicodeDisplayNameAndKeepsSlug(t *testing.T) {
	fake := newMCPAdminFake()
	h := newMCPHandler(fake)

	for _, body := range []string{
		`{"name":"Linear MCP 线性","slug":"linear","url":"https://mcp.linear.app/mcp"}`,
		`{"name":"Linear 问题追踪器","slug":"linear","url":"https://mcp.linear.app/mcp"}`,
	} {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", body)
		err := h.UpsertMCPServer(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	stored := fake.stored["linear"]
	assert.Equal(t, "linear", stored.Name)
	assert.Equal(t, "Linear 问题追踪器", stored.DisplayName)
}

func TestUpsertMCPServer_Rejections(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "stdio transport",
			body: `{"name":"local","transport":"stdio"}`,
		},
		{
			name: "managed name is read-only",
			body: `{"name":"github","url":"https://api.githubcopilot.com/mcp/"}`,
		},
		{
			name: "missing name",
			body: `{"url":"https://mcp.notion.com/mcp"}`,
		},
		{
			name: "invalid slug",
			body: `{"name":"Notion MCP","slug":"Bad Name!","url":"https://mcp.notion.com/mcp"}`,
		},
		{
			name: "invalid url",
			body: `{"name":"notion","url":"ftp://mcp.notion.com"}`,
		},
		{
			name: "missing url",
			body: `{"name":"notion"}`,
		},
		{
			name: "redacted header without stored value",
			body: `{"name":"fresh","url":"https://mcp.example.com/mcp","headers":{"Authorization":"***"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMCPAdminFake()
			fake.addManaged(mcpgateway.ServerView{
				Spec: mcpgateway.ServerSpec{Name: "github", URL: "https://api.githubcopilot.com/mcp/", Transport: "http", Enabled: true},
			})
			h := newMCPHandler(fake)

			c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", tt.body)
			err := h.UpsertMCPServer(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "invalid_request_error")
			assert.Empty(t, fake.stored)
		})
	}
}

func TestUpsertMCPServer_BubblesProviderErrorOnStoreFailure(t *testing.T) {
	fake := newMCPAdminFake()
	fake.upsertErr = errors.New("disk full")
	h := newMCPHandler(fake)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/mcp-servers", `{"name":"notion","url":"https://mcp.notion.com/mcp"}`)
	err := h.UpsertMCPServer(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

func TestDeleteMCPServer(t *testing.T) {
	tests := []struct {
		name       string
		server     string
		wantStatus int
	}{
		{name: "stored row", server: "notion", wantStatus: http.StatusNoContent},
		{name: "unknown name", server: "missing", wantStatus: http.StatusNotFound},
		{name: "managed name is read-only", server: "github", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMCPAdminFake()
			fake.addManaged(mcpgateway.ServerView{
				Spec: mcpgateway.ServerSpec{Name: "github", URL: "https://api.githubcopilot.com/mcp/", Transport: "http", Enabled: true},
			})
			fake.addStored(mcpgateway.ManagedServer{
				Name: "notion", URL: "https://mcp.notion.com/mcp", Transport: "http", Enabled: true,
			}, mcpgateway.StatusConnected)
			h := newMCPHandler(fake)

			c, rec := echotest.Request(t, http.MethodDelete, "/admin/mcp-servers/"+tt.server, nil, echotest.WithPathValue("name", tt.server))
			err := h.DeleteMCPServer(c)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Contains(t, fake.views, "github", "managed view must survive")
			if tt.wantStatus == http.StatusNoContent {
				assert.NotContains(t, fake.stored, "notion")
			} else {
				assert.Contains(t, fake.stored, "notion")
			}
		})
	}
}

func TestReconnectMCPServer(t *testing.T) {
	newFake := func() *mcpAdminFake {
		fake := newMCPAdminFake()
		fake.addStored(mcpgateway.ManagedServer{
			Name: "notion", URL: "https://mcp.notion.com/mcp", Transport: "http", Enabled: true,
		}, mcpgateway.StatusDegraded)
		return fake
	}

	t.Run("unknown name is 404", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/missing/reconnect", nil, echotest.WithPathValue("name", "missing"))
		err := h.ReconnectMCPServer(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("successful redial returns fresh view", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/reconnect", nil, echotest.WithPathValue("name", "notion"))
		err := h.ReconnectMCPServer(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		view := echotest.Decode[mcpServerViewResponse](t, rec)
		assert.Equal(t, "notion", view.Name)
		assert.Equal(t, string(mcpgateway.StatusConnected), view.Status)
	})

	t.Run("failed redial still returns 200 with last_error", func(t *testing.T) {
		fake := newFake()
		fake.reconnectErr = errors.New("dial tcp: connection refused")
		h := newMCPHandler(fake)
		c, rec := echotest.Request(t, http.MethodPost, "/admin/mcp-servers/notion/reconnect", nil, echotest.WithPathValue("name", "notion"))
		err := h.ReconnectMCPServer(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		view := echotest.Decode[mcpServerViewResponse](t, rec)
		assert.Equal(t, string(mcpgateway.StatusDegraded), view.Status)
		assert.Contains(t, view.LastError, "connection refused")
	})
}

func TestMCPServerCatalog(t *testing.T) {
	newFake := func() *mcpAdminFake {
		fake := newMCPAdminFake()
		fake.addStored(mcpgateway.ManagedServer{
			Name: "github", URL: "https://mcp.example.com/mcp", Transport: "http", Enabled: true,
		}, mcpgateway.StatusConnected)
		fake.catalogs = map[string]mcpgateway.CatalogView{
			"github": {
				Tools:   []mcpgateway.CatalogFeature{{Name: "create_issue", Description: "Create an issue"}},
				Prompts: []mcpgateway.CatalogFeature{{Name: "triage"}},
				Resources: []mcpgateway.CatalogResource{
					{URI: "repo://readme", Name: "readme"},
				},
				Templates: []mcpgateway.CatalogTemplate{},
			},
		}
		return fake
	}

	t.Run("unavailable service is 503", func(t *testing.T) {
		h := NewHandler(nil, nil)
		c, rec := echotest.Request(t, http.MethodGet, "/admin/mcp-servers/github/catalog", nil, echotest.WithPathValue("name", "github"))
		err := h.MCPServerCatalog(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	})

	t.Run("unknown name is 404", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := echotest.Request(t, http.MethodGet, "/admin/mcp-servers/missing/catalog", nil, echotest.WithPathValue("name", "missing"))
		err := h.MCPServerCatalog(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("returns the catalog snapshot", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := echotest.Request(t, http.MethodGet, "/admin/mcp-servers/github/catalog", nil, echotest.WithPathValue("name", "github"))
		err := h.MCPServerCatalog(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		catalog := echotest.Decode[mcpgateway.CatalogView](t, rec)
		assert.Equal(t, "github", catalog.Server)
		assert.Equal(t, mcpgateway.StatusConnected, catalog.Status)
		require.Len(t, catalog.Tools, 1)
		assert.Equal(t, "create_issue", catalog.Tools[0].Name)
		assert.Len(t, catalog.Prompts, 1)
		assert.Len(t, catalog.Resources, 1)
	})
}
