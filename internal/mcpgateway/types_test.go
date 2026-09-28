package mcpgateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/config"
)

func TestSpecFromConfigMapsAccessPolicy(t *testing.T) {
	t.Parallel()
	disabled := false
	cfg := config.MCPServerConfig{
		URL:                 "https://mcp.example.com/mcp",
		Transport:           config.MCPTransportHTTP,
		Headers:             map[string]string{"Authorization": "Bearer x"},
		Description:         "GitHub tools",
		Enabled:             &disabled,
		AllowedTools:        []string{"search"},
		DisallowedTools:     []string{"delete_repo"},
		UserPaths:           []string{"/eng/", "/eng"},
		DisallowedUserPaths: []string{" eng/contractors "},
		ToolTimeout:         45 * time.Second,
	}

	spec := SpecFromConfig("github", cfg)

	assert.Equal(t, "github", spec.Name)
	assert.Equal(t, "github", spec.DisplayName)
	assert.Equal(t, "https://mcp.example.com/mcp", spec.URL)
	assert.Equal(t, "GitHub tools", spec.Description)
	assert.False(t, spec.Enabled)
	assert.True(t, spec.Managed)
	assert.Equal(t, 45*time.Second, spec.ToolTimeout)
	assert.Equal(t, []string{"search"}, spec.AllowedTools)
	assert.Equal(t, []string{"delete_repo"}, spec.DisallowedTools)
	assert.Equal(t, []string{"/eng"}, spec.UserPaths)
	assert.Equal(t, []string{"/eng/contractors"}, spec.DisallowedUserPaths)
	assert.False(t, spec.visibleTo("/eng/contractors/acme"), "config-declared exclusions must reach the runtime spec")

	// The spec owns its lists: later edits to the config must not leak in.
	cfg.AllowedTools[0] = "mutated"
	cfg.Headers["Authorization"] = "mutated"
	assert.Equal(t, []string{"search"}, spec.AllowedTools)
	assert.Equal(t, "Bearer x", spec.Headers["Authorization"])
}
