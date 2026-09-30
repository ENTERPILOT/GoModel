package mcpgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchTerms(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{input: "github_create_issue", want: []string{"github", "create", "issue"}},
		{input: "listPullRequests", want: []string{"list", "pull", "requests"}},
		{input: "Search the web, fast!", want: []string{"search", "web", "fast"}},
		{input: "a b", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, searchTerms(tt.input))
		})
	}
}

func testIndex() *toolIndex {
	idx := &toolIndex{}
	schema := func(props ...string) map[string]any {
		properties := make(map[string]any, len(props))
		for _, prop := range props {
			properties[prop] = map[string]any{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": properties}
	}
	idx.add("github_create_issue", "github", &mcp.Tool{Name: "create_issue", Description: "Create a new issue in a repository", InputSchema: schema("owner", "repo", "title")})
	idx.add("github_list_issues", "github", &mcp.Tool{Name: "list_issues", Description: "List issues in a repository", InputSchema: schema("owner", "repo")})
	idx.add("github_merge_pull_request", "github", &mcp.Tool{Name: "merge_pull_request", Description: "Merge a pull request", InputSchema: schema("owner", "repo", "pullNumber")})
	idx.add("jira_create_ticket", "jira", &mcp.Tool{Name: "create_ticket", Description: "Create a Jira ticket for an issue", InputSchema: schema("project", "summary")})
	return idx
}

func exposedNames(tools []indexedTool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.exposed
	}
	return names
}

func TestRankTools(t *testing.T) {
	idx := testIndex()
	tests := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		{name: "more matched terms rank higher", query: "create issue", limit: 5, want: []string{"github_create_issue", "jira_create_ticket", "github_list_issues"}},
		{name: "plural matches singular", query: "issues", limit: 1, want: []string{"github_create_issue"}},
		{name: "parameter names are searchable", query: "pull number", limit: 5, want: []string{"github_merge_pull_request"}},
		{name: "exact bare name wins", query: "create_ticket", limit: 1, want: []string{"jira_create_ticket"}},
		{name: "limit truncates", query: "github", limit: 2, want: []string{"github_create_issue", "github_list_issues"}},
		{name: "short tool terms do not match long query terms", query: "weather forecast", limit: 5, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exposedNames(rankTools(tt.query, idx.tools, tt.limit)))
		})
	}
}

func TestToolArguments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "absent", input: "", want: ""},
		{name: "null", input: "null", want: ""},
		{name: "object", input: ` {"a":1} `, want: `{"a":1}`},
		{name: "object encoded as string", input: `"{\"a\":1}"`, want: `{"a":1}`},
		{name: "plain string passes through", input: `"hello"`, want: `"hello"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, string(toolArguments(json.RawMessage(tt.input))))
		})
	}
}

func searchTools(t *testing.T, session *mcp.ClientSession, query string) []searchResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      searchToolsName,
		Arguments: map[string]any{"query": query},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "search_tools failed: %#v", result.Content)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	if !strings.HasPrefix(text.Text, "{") {
		return nil
	}
	var payload struct {
		Tools []searchResult `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	return payload.Tools
}

func TestSearchDiscoveryServesMetaToolsAndRelaysCalls(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	usageLog := &recordingUsageLogger{}
	service, gatewayURL := newTestService(t, usageLog,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)
	service.searchDiscovery = true

	session := connectClient(t, gatewayURL+"/mcp", map[string]string{"X-Request-ID": "req-1"})
	assert.Equal(t, []string{CallToolName, searchToolsName}, listToolNames(t, session))
	assert.Contains(t, session.InitializeResult().Instructions, searchToolsName)

	results := searchTools(t, session, "echo")
	require.Len(t, results, 2, "beta_search matches through its description")
	assert.Equal(t, "alpha_echo", results[0].Name, "a name match ranks first")
	assert.Equal(t, "echoes its input", results[0].Description)
	assert.NotNil(t, results[0].InputSchema)

	for _, name := range []string{"alpha_echo", "echo"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      CallToolName,
			Arguments: map[string]any{"name": name, "arguments": map[string]any{"value": 1}},
		})
		require.NoError(t, err)
		require.False(t, result.IsError)
		text, ok := result.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, `echo:{"value":1}`, text.Text)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(usageLog.all()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	entries := usageLog.all()
	require.Len(t, entries, 2, "only relayed calls are usage entries, not searches")
	for _, entry := range entries {
		assert.Equal(t, "alpha_echo", entry.Model)
		assert.Equal(t, "alpha", entry.ProviderName)
		assert.Equal(t, "req-1", entry.RequestID)
	}
}

func TestSearchDiscoveryRejectsUnknownToolAsToolError(t *testing.T) {
	url := newTestUpstream(t, "alpha", addEchoTool("echo"))
	service, gatewayURL := newTestService(t, nil, testSpec("alpha", url, nil))
	service.searchDiscovery = true

	session := connectClient(t, gatewayURL+"/mcp", nil)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      CallToolName,
		Arguments: map[string]any{"name": "alpha_missing"},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      searchToolsName,
		Arguments: map[string]any{"query": " "},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestToolDiscoveryHeaderOverridesDefault(t *testing.T) {
	url := newTestUpstream(t, "alpha", addEchoTool("echo"))
	service, gatewayURL := newTestService(t, nil, testSpec("alpha", url, nil))

	optIn := connectClient(t, gatewayURL+"/mcp", map[string]string{ToolDiscoveryHeader: "Search"})
	assert.Equal(t, []string{CallToolName, searchToolsName}, listToolNames(t, optIn))

	unknown := connectClient(t, gatewayURL+"/mcp", map[string]string{ToolDiscoveryHeader: "semantic"})
	assert.Equal(t, []string{"alpha_echo"}, listToolNames(t, unknown))

	service.searchDiscovery = true
	optOut := connectClient(t, gatewayURL+"/mcp", map[string]string{ToolDiscoveryHeader: "off"})
	assert.Equal(t, []string{"alpha_echo"}, listToolNames(t, optOut))

	pinned := connectClient(t, gatewayURL+"/mcp/alpha", nil)
	assert.Equal(t, []string{CallToolName, searchToolsName}, listToolNames(t, pinned))
	results := searchTools(t, pinned, "echo")
	require.NotEmpty(t, results)
	assert.Equal(t, "echo", results[0].Name, "a pinned endpoint keeps original names")
}

func TestSearchDiscoveryHidesToolsExcludedAfterInitialize(t *testing.T) {
	url := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		addEchoTool("read")(server)
		addEchoTool("write")(server)
	})
	spec := testSpec("alpha", url, nil)
	service, gatewayURL := newTestService(t, nil, spec)
	service.searchDiscovery = true

	session := connectClient(t, gatewayURL+"/mcp", nil)
	require.Len(t, searchTools(t, session, "echoes"), 2)

	spec.DisallowedTools = []string{"write"}
	service.manager.Apply([]ServerSpec{spec})

	results := searchTools(t, session, "echoes")
	require.Len(t, results, 1)
	assert.Equal(t, "alpha_read", results[0].Name)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      CallToolName,
		Arguments: map[string]any{"name": "alpha_write"},
	})
	require.NoError(t, err, "failures reach the model as tool errors, not JSON-RPC errors")
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "excluded")
}

func TestSearchDiscoveryReportsUpstreamFailureAsToolError(t *testing.T) {
	url := newTestUpstream(t, "alpha", addEchoTool("echo"))
	usageLog := &recordingUsageLogger{}
	service, gatewayURL := newTestService(t, usageLog, testSpec("alpha", url, nil))
	service.searchDiscovery = true

	session := connectClient(t, gatewayURL+"/mcp", nil)
	service.manager.Apply(nil) // the upstream disappears after initialize

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      CallToolName,
		Arguments: map[string]any{"name": "alpha_echo"},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
