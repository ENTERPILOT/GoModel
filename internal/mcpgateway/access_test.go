package mcpgateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAccessSelectors(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		want    []string
		wantErr string
	}{
		{name: "empty is unrestricted", raw: nil, want: nil},
		{name: "blank entries only", raw: []string{" ", ""}, want: nil},
		{name: "bare slug means whole server", raw: []string{"exa"}, want: []string{"exa/*"}},
		{name: "tool and wildcard kept", raw: []string{" exa/web_search_exa ", "github/*"}, want: []string{"exa/web_search_exa", "github/*"}},
		{name: "duplicates collapse", raw: []string{"exa", "exa/*", "exa/a", "exa/a"}, want: []string{"exa/*", "exa/a"}},
		{name: "tool names keep their case and slashes", raw: []string{"fs/Read/File"}, want: []string{"fs/Read/File"}},
		{name: "missing tool", raw: []string{"exa/"}, wantErr: "tool name is required"},
		{name: "invalid slug", raw: []string{"Exa Search/x"}, wantErr: "invalid MCP selector"},
		{name: "global wildcard is not a selector", raw: []string{"*"}, wantErr: "invalid MCP selector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAccessSelectors(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKeyAccess(t *testing.T) {
	access := newKeyAccess([]string{"github/*", "exa/web_search_exa", "bad slug/x"})
	tests := []struct {
		name                         string
		access                       *keyAccess
		server, tool                 string
		wantServer, wantWhole, wantT bool
	}{
		{name: "unrestricted", access: nil, server: "any", tool: "thing", wantServer: true, wantWhole: true, wantT: true},
		{name: "whole server grant", access: access, server: "github", tool: "create_issue", wantServer: true, wantWhole: true, wantT: true},
		{name: "tool grant", access: access, server: "exa", tool: "web_search_exa", wantServer: true, wantWhole: false, wantT: true},
		{name: "sibling tool", access: access, server: "exa", tool: "web_fetch_exa", wantServer: true, wantWhole: false, wantT: false},
		{name: "ungranted server", access: access, server: "jira", tool: "create_ticket"},
		{name: "invalid entries grant nothing", access: newKeyAccess([]string{"bad slug"}), server: "bad slug", tool: "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantServer, tt.access.server(tt.server))
			assert.Equal(t, tt.wantWhole, tt.access.wholeServer(tt.server))
			assert.Equal(t, tt.wantT, tt.access.tool(tt.server, tt.tool))
		})
	}
}

func TestAccessCovers(t *testing.T) {
	granted := []string{"github/*", "exa/web_search_exa"}
	tests := []struct {
		name     string
		granted  []string
		selector string
		want     bool
	}{
		{name: "unrestricted caller covers anything", granted: nil, selector: "jira/*", want: true},
		{name: "unrestricted caller covers unrestricted", granted: nil, selector: "", want: true},
		{name: "restricted caller cannot grant unrestricted", granted: granted, selector: "", want: false},
		{name: "whole server within whole server", granted: granted, selector: "github/*", want: true},
		{name: "tool within whole server", granted: granted, selector: "github/create_issue", want: true},
		{name: "same tool", granted: granted, selector: "exa/web_search_exa", want: true},
		{name: "whole server wider than tool grant", granted: granted, selector: "exa/*", want: false},
		{name: "sibling tool", granted: granted, selector: "exa/web_fetch_exa", want: false},
		{name: "other server", granted: granted, selector: "jira/*", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AccessCovers(tt.granted, tt.selector))
		})
	}
}

func newAllowlistTestService(t *testing.T) (*Service, string) {
	t.Helper()
	alphaURL := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		addEchoTool("read")(server)
		addEchoTool("write")(server)
		server.AddPrompt(&mcp.Prompt{Name: "greet"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	})
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	return newTestService(t, nil, testSpec("alpha", alphaURL, nil), testSpec("beta", betaURL, nil))
}

func TestKeyAllowlistFiltersCatalogAndCalls(t *testing.T) {
	_, gatewayURL := newAllowlistTestService(t)
	headers := map[string]string{testAllowedMCPHeader: "alpha/read"}

	session := connectClient(t, gatewayURL+"/mcp", headers)
	assert.Equal(t, []string{"alpha_read"}, listToolNames(t, session))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_read"})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_write"})
	require.Error(t, err, "a tool outside the allowlist must not be callable")

	prompts, err := session.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, prompts.Prompts, "a single-tool grant must not expose the server's prompts")

	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp/beta", testInitializeBody, headers))
	assert.Equal(t, http.StatusOK, rawMCPStatus(t, gatewayURL+"/mcp/alpha", testInitializeBody, headers))

	wide := connectClient(t, gatewayURL+"/mcp", map[string]string{testAllowedMCPHeader: "alpha/*,beta/*"})
	assert.Equal(t, []string{"alpha_read", "alpha_write", "beta_search"}, listToolNames(t, wide))
	prompts, err = wide.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, prompts.Prompts, 1)

	unrestricted := connectClient(t, gatewayURL+"/mcp", nil)
	assert.Len(t, listToolNames(t, unrestricted), 3)
}

func TestKeyAllowlistAppliesToSearchDiscovery(t *testing.T) {
	service, gatewayURL := newAllowlistTestService(t)
	service.searchDiscovery = true

	session := connectClient(t, gatewayURL+"/mcp", map[string]string{testAllowedMCPHeader: "alpha/read"})
	results := searchTools(t, session, "echoes input")
	require.Len(t, results, 1)
	assert.Equal(t, "alpha_read", results[0].Name)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      callToolName,
		Arguments: map[string]any{"name": "alpha_write"},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "call_tool must not reach a tool outside the allowlist")
}

func TestKeyAccessEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{name: "both unrestricted", want: true},
		{name: "restricted vs unrestricted", a: []string{"alpha/*"}, want: false},
		{name: "same grants in another order", a: []string{"alpha/*", "beta/x"}, b: []string{"beta/x", "alpha/*"}, want: true},
		{name: "narrowed", a: []string{"alpha/*"}, b: []string{"alpha/read"}, want: false},
		{name: "other tool", a: []string{"alpha/read"}, b: []string{"alpha/write"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newKeyAccess(tt.a).equal(newKeyAccess(tt.b)))
		})
	}
}

func TestKeyAllowlistEditEndsOpenSessions(t *testing.T) {
	_, gatewayURL := newAllowlistTestService(t)
	const listTools = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	wide := map[string]string{testAllowedMCPHeader: "alpha/*"}

	sessionID := initializeRawSession(t, gatewayURL+"/mcp", wide)
	withSession := func(allowed string) map[string]string {
		return map[string]string{testAllowedMCPHeader: allowed, "Mcp-Session-Id": sessionID}
	}
	assert.NotEqual(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp", listTools, withSession("alpha/*")),
		"an unchanged allowlist keeps the session")

	// The key was narrowed: the session's catalog is stale, so it ends and the
	// client re-initializes.
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp", listTools, withSession("alpha/read")))
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp", listTools, withSession("alpha/*")),
		"an ended session stays ended")

	fresh := connectClient(t, gatewayURL+"/mcp", map[string]string{testAllowedMCPHeader: "alpha/read"})
	assert.Equal(t, []string{"alpha_read"}, listToolNames(t, fresh))
}

func TestAuthorizeSessionChecksKeyAllowlist(t *testing.T) {
	service, _ := newAllowlistTestService(t)

	service.bindSession("open", requestScope{authKeyID: "key-1", access: newKeyAccess([]string{"alpha/read"})}, nil, nil)
	require.NoError(t, service.authorizeSessionID("open", "alpha", "read"))
	require.ErrorIs(t, service.authorizeSessionID("open", "alpha", "write"), ErrNotAllowedForKey)
	require.ErrorIs(t, service.authorizeSessionID("open", "alpha", ""), ErrNotAllowedForKey)
	require.ErrorIs(t, service.authorizeSessionID("open", "beta", "search"), ErrNotAllowedForKey)
}
