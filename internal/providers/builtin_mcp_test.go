package providers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestProviderMCPServers(t *testing.T) {
	factory := NewProviderFactory()
	newProvider := func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} }
	factory.Add(Registration{
		Type: "hosted",
		New:  newProvider,
		MCPServers: func(baseURL string) []BuiltinMCPServer {
			return []BuiltinMCPServer{{Suffix: "search", URL: baseURL + "/mcp", Description: "Search"}}
		},
	})
	factory.Add(Registration{Type: "plain", New: newProvider})

	resolved := map[string]config.RawProviderConfig{
		"Hosted EU": {Type: "hosted", APIKey: "key-eu", BaseURL: "https://eu.example.com"},
		"plain":     {Type: "plain", APIKey: "key-plain"},
		"keyless":   {Type: "hosted", BaseURL: "https://local.example.com"},
	}

	tests := []struct {
		name  string
		names []string
		want  map[string]config.MCPServerConfig
	}{
		{name: "none requested", names: nil, want: map[string]config.MCPServerConfig{}},
		{name: "unknown provider is skipped", names: []string{"missing"}, want: map[string]config.MCPServerConfig{}},
		{name: "type without hosted servers is skipped", names: []string{"plain"}, want: map[string]config.MCPServerConfig{}},
		{name: "provider without a key is skipped", names: []string{"keyless"}, want: map[string]config.MCPServerConfig{}},
		{
			name:  "hosted servers use the provider key and a derived slug",
			names: []string{"Hosted EU", "plain"},
			want: map[string]config.MCPServerConfig{
				"hosted-eu-search": {
					URL:         "https://eu.example.com/mcp",
					Transport:   config.MCPTransportHTTP,
					Headers:     map[string]string{"Authorization": "Bearer key-eu"},
					Description: "Search",
					ToolTimeout: config.DefaultMCPToolTimeout,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, providerMCPServers(factory, resolved, tt.names))
		})
	}
}

func TestInitResultProviderMCPServersWithoutProviders(t *testing.T) {
	var result *InitResult
	assert.Nil(t, result.ProviderMCPServers([]string{"zai"}))
}

func TestAddWithoutMCPServersClearsPreviousRegistration(t *testing.T) {
	factory := NewProviderFactory()
	newProvider := func(ProviderConfig, ProviderOptions) core.Provider { return &registryMockProvider{} }
	factory.Add(Registration{Type: "hosted", New: newProvider, MCPServers: func(string) []BuiltinMCPServer {
		return []BuiltinMCPServer{{Suffix: "search", URL: "https://example.com/mcp"}}
	}})
	factory.Add(Registration{Type: "hosted", New: newProvider})
	assert.Empty(t, factory.builtinMCPServers("hosted", ""))
}
