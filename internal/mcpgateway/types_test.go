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

func TestSpecFromConfigMapsToolCache(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		URL:   "https://mcp.example.com/mcp",
		Cache: config.MCPToolCacheConfig{TTL: 300, Tools: []string{"search"}},
	}

	spec := SpecFromConfig("docs", cfg)

	assert.Equal(t, 5*time.Minute, spec.CacheTTL)
	assert.Equal(t, []string{"search"}, spec.CacheTools)
	assert.Zero(t, SpecFromConfig("plain", config.MCPServerConfig{URL: cfg.URL}).CacheTTL, "caching is off unless configured")

	// The spec owns its list: later edits to the config must not leak in.
	cfg.Cache.Tools[0] = "mutated"
	assert.Equal(t, []string{"search"}, spec.CacheTools)
	cfg.Cache.Tools[0] = "search"

	// A changed cache policy is a changed spec, but gateway policy only: it
	// is applied in place, without a redial.
	cfg.Cache.TTL = 60
	shorter := SpecFromConfig("docs", cfg)
	assert.False(t, spec.equal(shorter))
	assert.True(t, spec.withoutAccessPolicy().equal(shorter.withoutAccessPolicy()))
	cfg.Cache = config.MCPToolCacheConfig{TTL: 300, Tools: []string{"fetch"}}
	otherTools := SpecFromConfig("docs", cfg)
	assert.False(t, spec.equal(otherTools))
	assert.True(t, spec.withoutAccessPolicy().equal(otherTools.withoutAccessPolicy()))
}
