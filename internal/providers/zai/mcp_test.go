package zai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPServersFollowBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		wantRoot string
	}{
		{name: "default", baseURL: "", wantRoot: "https://api.z.ai/api"},
		{name: "coding plan", baseURL: "https://api.z.ai/api/coding/paas/v4", wantRoot: "https://api.z.ai/api"},
		{name: "mainland host", baseURL: "https://open.bigmodel.cn/api/paas/v4/", wantRoot: "https://open.bigmodel.cn/api"},
		{name: "path-prefixed proxy", baseURL: "http://proxy.internal:8080/zai/api/paas/v4", wantRoot: "http://proxy.internal:8080/zai/api"},
		{name: "no api segment", baseURL: "https://proxy.example.com/v4", wantRoot: "https://proxy.example.com/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			servers := mcpServers(tt.baseURL)
			require.Len(t, servers, 3)
			urls := map[string]string{}
			for _, server := range servers {
				assert.NotEmpty(t, server.Description)
				urls[server.Suffix] = server.URL
			}
			assert.Equal(t, map[string]string{
				"web-search": tt.wantRoot + "/mcp/web_search_prime/mcp",
				"web-reader": tt.wantRoot + "/mcp/web_reader/mcp",
				"zread":      tt.wantRoot + "/mcp/zread/mcp",
			}, urls)
		})
	}
}

func TestMCPServersRejectUnusableBaseURL(t *testing.T) {
	for _, baseURL := range []string{"api.z.ai/api/paas/v4", "ftp://api.z.ai/api", "://bad"} {
		assert.Empty(t, mcpServers(baseURL), baseURL)
	}
}

func TestRegistrationExposesMCPServers(t *testing.T) {
	require.NotNil(t, Registration.MCPServers)
}
