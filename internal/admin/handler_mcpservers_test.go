package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

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

func newMCPServerContext(method, target, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func newMCPServerNameContext(method, target, name string) (*echo.Context, *httptest.ResponseRecorder) {
	c, rec := newMCPServerContext(method, target, "")
	c.SetPathValues(echo.PathValues{{Name: "name", Value: name}})
	return c, rec
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

	c, rec := newMCPServerContext(http.MethodGet, "/admin/mcp-servers", "")
	if err := h.ListMCPServers(c); err != nil {
		t.Fatalf("ListMCPServers() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"top-secret", "sk-hidden"} {
		if containsString(rec.Body.String(), secret) {
			t.Fatalf("response leaked header value %q: %s", secret, rec.Body.String())
		}
	}

	var body []mcpServerViewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("len(body) = %d, want 2 (%#v)", len(body), body)
	}
	byName := map[string]mcpServerViewResponse{}
	for _, view := range body {
		byName[view.Name] = view
	}

	github := byName["github"]
	if !github.Managed {
		t.Fatalf("github.Managed = false, want true (config-declared)")
	}
	if got := github.Headers["Authorization"]; got != "***" {
		t.Fatalf("github Authorization header = %q, want ***", got)
	}
	if github.Status != string(mcpgateway.StatusConnected) || github.ToolCount != 3 {
		t.Fatalf("github view = %#v, want connected with 3 tools", github)
	}
	if github.ConnectedAt == nil || !github.ConnectedAt.Equal(connectedAt) {
		t.Fatalf("github.ConnectedAt = %v, want %v", github.ConnectedAt, connectedAt)
	}

	notion := byName["notion"]
	if notion.Managed {
		t.Fatalf("notion.Managed = true, want false (admin-store row)")
	}
	if got := notion.Headers["X-Api-Key"]; got != "***" {
		t.Fatalf("notion X-Api-Key header = %q, want ***", got)
	}
	if notion.ConnectedAt != nil {
		t.Fatalf("notion.ConnectedAt = %v, want omitted for a never-connected server", notion.ConnectedAt)
	}
}

func TestMCPServerEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", name, rec.Code)
		}
		var body map[string]map[string]any
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatalf("%s decode error = %v", name, decodeErr)
		}
		if got := body["error"]["code"]; got != "feature_unavailable" {
			t.Fatalf("%s error code = %v, want feature_unavailable", name, got)
		}
	}

	listCtx, listRec := newMCPServerContext(http.MethodGet, "/admin/mcp-servers", "")
	assertUnavailable("ListMCPServers", h.ListMCPServers(listCtx), listRec)

	putCtx, putRec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", `{"name":"notion","url":"https://mcp.notion.com/mcp"}`)
	assertUnavailable("UpsertMCPServer", h.UpsertMCPServer(putCtx), putRec)

	deleteCtx, deleteRec := newMCPServerNameContext(http.MethodDelete, "/admin/mcp-servers/notion", "notion")
	assertUnavailable("DeleteMCPServer", h.DeleteMCPServer(deleteCtx), deleteRec)

	reconnectCtx, reconnectRec := newMCPServerNameContext(http.MethodPost, "/admin/mcp-servers/notion/reconnect", "notion")
	assertUnavailable("ReconnectMCPServer", h.ReconnectMCPServer(reconnectCtx), reconnectRec)
}

func TestUpsertMCPServer_CreatesAndReturnsRedactedView(t *testing.T) {
	fake := newMCPAdminFake()
	h := newMCPHandler(fake)

	body := `{"name":"notion","url":"https://mcp.notion.com/mcp","headers":{"Authorization":"Bearer real-token"},"description":"notes","user_paths":["/team"]}`
	c, rec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", body)
	if err := h.UpsertMCPServer(c); err != nil {
		t.Fatalf("UpsertMCPServer() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if containsString(rec.Body.String(), "real-token") {
		t.Fatalf("upsert response leaked header value: %s", rec.Body.String())
	}

	var view mcpServerViewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if view.Name != "notion" || view.URL != "https://mcp.notion.com/mcp" {
		t.Fatalf("view = %#v, want notion", view)
	}
	if view.Transport != "http" {
		t.Fatalf("view.Transport = %q, want default http", view.Transport)
	}
	if !view.Enabled {
		t.Fatalf("view.Enabled = false, want default true")
	}
	if got := view.Headers["Authorization"]; got != "***" {
		t.Fatalf("view Authorization header = %q, want ***", got)
	}

	stored, ok := fake.stored["notion"]
	if !ok {
		t.Fatalf("upsert did not reach the service; stored = %#v", fake.stored)
	}
	if got := stored.Headers["Authorization"]; got != "Bearer real-token" {
		t.Fatalf("stored Authorization header = %q, want the real value", got)
	}
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
	c, rec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", body)
	if err := h.UpsertMCPServer(c); err != nil {
		t.Fatalf("UpsertMCPServer() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	stored := fake.stored["notion"]
	if got := stored.Headers["Authorization"]; got != "Bearer original" {
		t.Fatalf("stored Authorization = %q, want preserved original", got)
	}
	if got := stored.Headers["X-Region"]; got != "us" {
		t.Fatalf("stored X-Region = %q, want us", got)
	}
	if stored.Enabled {
		t.Fatalf("stored.Enabled = true, want false (preserved when omitted)")
	}
}

func TestUpsertMCPServer_AllowsUnicodeDisplayNameAndKeepsSlug(t *testing.T) {
	fake := newMCPAdminFake()
	h := newMCPHandler(fake)

	for _, body := range []string{
		`{"name":"Linear MCP 线性","slug":"linear","url":"https://mcp.linear.app/mcp"}`,
		`{"name":"Linear 问题追踪器","slug":"linear","url":"https://mcp.linear.app/mcp"}`,
	} {
		c, rec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", body)
		if err := h.UpsertMCPServer(c); err != nil {
			t.Fatalf("UpsertMCPServer() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
	}

	stored := fake.stored["linear"]
	if stored.Name != "linear" || stored.DisplayName != "Linear 问题追踪器" {
		t.Fatalf("stored identity = (%q, %q), want immutable slug and edited display name", stored.Name, stored.DisplayName)
	}
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

			c, rec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", tt.body)
			if err := h.UpsertMCPServer(c); err != nil {
				t.Fatalf("UpsertMCPServer() error = %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
			if !containsString(rec.Body.String(), "invalid_request_error") {
				t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
			}
			if len(fake.stored) != 0 {
				t.Fatalf("rejected upsert reached the service: %#v", fake.stored)
			}
		})
	}
}

func TestUpsertMCPServer_BubblesProviderErrorOnStoreFailure(t *testing.T) {
	fake := newMCPAdminFake()
	fake.upsertErr = errors.New("disk full")
	h := newMCPHandler(fake)

	c, rec := newMCPServerContext(http.MethodPut, "/admin/mcp-servers", `{"name":"notion","url":"https://mcp.notion.com/mcp"}`)
	if err := h.UpsertMCPServer(c); err != nil {
		t.Fatalf("UpsertMCPServer() error = %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 body=%s", rec.Code, rec.Body.String())
	}
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

			c, rec := newMCPServerNameContext(http.MethodDelete, "/admin/mcp-servers/"+tt.server, tt.server)
			if err := h.DeleteMCPServer(c); err != nil {
				t.Fatalf("DeleteMCPServer() error = %v", err)
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if _, managedKept := fake.views["github"]; !managedKept {
				t.Fatalf("managed server was removed")
			}
			if _, storedKept := fake.stored["notion"]; storedKept == (tt.wantStatus == http.StatusNoContent) {
				t.Fatalf("stored row presence = %v after status %d", storedKept, rec.Code)
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
		c, rec := newMCPServerNameContext(http.MethodPost, "/admin/mcp-servers/missing/reconnect", "missing")
		if err := h.ReconnectMCPServer(c); err != nil {
			t.Fatalf("ReconnectMCPServer() error = %v", err)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("successful redial returns fresh view", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := newMCPServerNameContext(http.MethodPost, "/admin/mcp-servers/notion/reconnect", "notion")
		if err := h.ReconnectMCPServer(c); err != nil {
			t.Fatalf("ReconnectMCPServer() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		var view mcpServerViewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if view.Name != "notion" || view.Status != string(mcpgateway.StatusConnected) {
			t.Fatalf("view = %#v, want connected notion", view)
		}
	})

	t.Run("failed redial still returns 200 with last_error", func(t *testing.T) {
		fake := newFake()
		fake.reconnectErr = errors.New("dial tcp: connection refused")
		h := newMCPHandler(fake)
		c, rec := newMCPServerNameContext(http.MethodPost, "/admin/mcp-servers/notion/reconnect", "notion")
		if err := h.ReconnectMCPServer(c); err != nil {
			t.Fatalf("ReconnectMCPServer() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		var view mcpServerViewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if view.Status != string(mcpgateway.StatusDegraded) {
			t.Fatalf("view.Status = %q, want degraded", view.Status)
		}
		if !containsString(view.LastError, "connection refused") {
			t.Fatalf("view.LastError = %q, want the redial failure", view.LastError)
		}
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
		c, rec := newMCPServerNameContext(http.MethodGet, "/admin/mcp-servers/github/catalog", "github")
		if err := h.MCPServerCatalog(c); err != nil {
			t.Fatalf("MCPServerCatalog() error = %v", err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown name is 404", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := newMCPServerNameContext(http.MethodGet, "/admin/mcp-servers/missing/catalog", "missing")
		if err := h.MCPServerCatalog(c); err != nil {
			t.Fatalf("MCPServerCatalog() error = %v", err)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("returns the catalog snapshot", func(t *testing.T) {
		h := newMCPHandler(newFake())
		c, rec := newMCPServerNameContext(http.MethodGet, "/admin/mcp-servers/github/catalog", "github")
		if err := h.MCPServerCatalog(c); err != nil {
			t.Fatalf("MCPServerCatalog() error = %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}
		var catalog mcpgateway.CatalogView
		if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if catalog.Server != "github" || catalog.Status != mcpgateway.StatusConnected {
			t.Fatalf("catalog = %+v, want connected github", catalog)
		}
		if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "create_issue" {
			t.Fatalf("catalog.Tools = %+v, want create_issue", catalog.Tools)
		}
		if len(catalog.Prompts) != 1 || len(catalog.Resources) != 1 {
			t.Fatalf("catalog prompts/resources = %+v / %+v", catalog.Prompts, catalog.Resources)
		}
	})
}
