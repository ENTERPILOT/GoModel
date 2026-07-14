// Package mcpgateway aggregates upstream MCP servers behind GoModel's
// authenticated /mcp endpoints. The gateway terminates the MCP protocol on
// both legs: it is an MCP server to clients and an MCP client to upstreams.
// It is also the credential boundary — client bearer tokens never reach an
// upstream; upstream credentials come only from server configuration.
package mcpgateway

import (
	"maps"
	"slices"
	"time"

	"github.com/enterpilot/gomodel/config"
)

// ServerStatus describes the runtime connection state of one upstream server.
type ServerStatus string

const (
	// StatusDisabled means the server is declared but switched off.
	StatusDisabled ServerStatus = "disabled"
	// StatusConnecting means no session has been established yet.
	StatusConnecting ServerStatus = "connecting"
	// StatusConnected means the session is live and the catalog is fresh.
	StatusConnected ServerStatus = "connected"
	// StatusDegraded means the last connect or listing failed; any previous
	// catalog is kept (stale carry-forward) and the server is re-probed.
	StatusDegraded ServerStatus = "degraded"
)

// ServerSpec is the runtime-normalized definition of one upstream server,
// merged from declarative config (Managed=true) and the admin store.
type ServerSpec struct {
	// Name is the stable ASCII slug used for routing and namespacing.
	Name        string
	DisplayName string

	URL             string
	Transport       string
	Headers         map[string]string
	Command         string
	Args            []string
	Env             map[string]string
	Description     string
	Enabled         bool
	AllowedTools    []string
	DisallowedTools []string
	UserPaths       []string
	ToolTimeout     time.Duration

	// Managed marks specs declared in config.yaml / MCP_SERVERS. They override
	// admin-store rows with the same name and are read-only in the dashboard.
	Managed bool
}

// SpecFromConfig converts one declarative config entry into a runtime spec.
func SpecFromConfig(name string, cfg config.MCPServerConfig) ServerSpec {
	return ServerSpec{
		Name:            name,
		DisplayName:     name,
		URL:             cfg.URL,
		Transport:       cfg.Transport,
		Headers:         maps.Clone(cfg.Headers),
		Command:         cfg.Command,
		Args:            slices.Clone(cfg.Args),
		Env:             maps.Clone(cfg.Env),
		Description:     cfg.Description,
		Enabled:         config.MCPServerEnabled(cfg),
		AllowedTools:    slices.Clone(cfg.AllowedTools),
		DisallowedTools: slices.Clone(cfg.DisallowedTools),
		UserPaths:       normalizeUserPaths(cfg.UserPaths),
		ToolTimeout:     cfg.ToolTimeout,
		Managed:         true,
	}
}

// equal reports whether two specs describe the same upstream configuration,
// so the manager can keep an existing session across no-op reloads.
func (s ServerSpec) equal(other ServerSpec) bool {
	return s.Name == other.Name &&
		s.DisplayName == other.DisplayName &&
		s.URL == other.URL &&
		s.Transport == other.Transport &&
		maps.Equal(s.Headers, other.Headers) &&
		s.Command == other.Command &&
		slices.Equal(s.Args, other.Args) &&
		maps.Equal(s.Env, other.Env) &&
		s.Description == other.Description &&
		s.Enabled == other.Enabled &&
		slices.Equal(s.AllowedTools, other.AllowedTools) &&
		slices.Equal(s.DisallowedTools, other.DisallowedTools) &&
		slices.Equal(s.UserPaths, other.UserPaths) &&
		s.ToolTimeout == other.ToolTimeout &&
		s.Managed == other.Managed
}

// ServerView is a point-in-time snapshot of one upstream for admin and
// dashboard consumption.
type ServerView struct {
	Spec        ServerSpec
	Status      ServerStatus
	LastError   string
	ToolCount   int
	PromptCount int
	// ResourceCount includes resource templates.
	ResourceCount int
	ConnectedAt   time.Time
}
