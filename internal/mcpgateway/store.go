package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
)

// ErrNotFound indicates a requested managed MCP server was not found.
var ErrNotFound = errors.New("mcp server not found")

// Store defines persistence operations for admin-managed MCP servers.
type Store interface {
	List(ctx context.Context) ([]ManagedServer, error)
	Get(ctx context.Context, name string) (*ManagedServer, error)
	Upsert(ctx context.Context, server ManagedServer) error
	Delete(ctx context.Context, name string) error
	Close() error
}

// ManagedServer is one admin-managed upstream server row. Stdio fields are
// deliberately absent: runtime-registered subprocesses are a remote code
// execution vector (see the gateway spec), so stdio servers exist only as
// declarative config.
type ManagedServer struct {
	// Name is the immutable ASCII slug and remains the storage primary key.
	Name        string `json:"slug"`
	DisplayName string `json:"name"`

	URL                string            `json:"url"`
	Transport          string            `json:"transport"`
	Headers            map[string]string `json:"headers,omitempty"`
	Description        string            `json:"description,omitempty"`
	Enabled            bool              `json:"enabled"`
	AllowedTools       []string          `json:"allowed_tools,omitempty"`
	DisallowedTools    []string          `json:"disallowed_tools,omitempty"`
	UserPaths          []string          `json:"user_paths,omitempty"`
	ToolTimeoutSeconds int               `json:"tool_timeout_seconds,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// Validate checks the row against the same rules as declarative config,
// additionally rejecting the stdio transport. The receiver is only mutated
// (normalized transport/URL) once every check has passed.
func (m *ManagedServer) Validate() error {
	m.Name = strings.ToLower(strings.TrimSpace(m.Name))
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	if m.DisplayName == "" {
		m.DisplayName = m.Name
	}
	if err := config.ValidateMCPServerName(m.DisplayName); err != nil {
		return err
	}
	if err := config.ValidateMCPServerSlug(m.Name); err != nil {
		return err
	}
	if m.Transport == config.MCPTransportStdio {
		return fmt.Errorf("stdio servers can only be declared in config.yaml or MCP_SERVERS, not via the admin API")
	}
	if m.ToolTimeoutSeconds < 0 {
		return fmt.Errorf("tool_timeout_seconds must not be negative")
	}
	cfg := config.MCPServerConfig{
		URL:         m.URL,
		Transport:   m.Transport,
		Headers:     m.Headers,
		UserPaths:   m.UserPaths,
		ToolTimeout: time.Duration(m.ToolTimeoutSeconds) * time.Second,
	}
	if err := config.ValidateMCPServerConfig(&cfg); err != nil {
		return err
	}
	m.Transport = cfg.Transport
	m.URL = cfg.URL
	m.UserPaths = cfg.UserPaths
	return nil
}

// Spec converts the row into a runtime spec.
func (m ManagedServer) Spec() ServerSpec {
	timeout := time.Duration(m.ToolTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = config.DefaultMCPToolTimeout
	}
	return ServerSpec{
		Name:            m.Name,
		DisplayName:     m.DisplayName,
		URL:             m.URL,
		Transport:       m.Transport,
		Headers:         maps.Clone(m.Headers),
		Description:     m.Description,
		Enabled:         m.Enabled,
		AllowedTools:    slices.Clone(m.AllowedTools),
		DisallowedTools: slices.Clone(m.DisallowedTools),
		UserPaths:       normalizeUserPaths(m.UserPaths),
		ToolTimeout:     timeout,
		Managed:         false,
	}
}

func encodeJSONMap(value map[string]string) (string, error) {
	if value == nil {
		value = map[string]string{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode map: %w", err)
	}
	return string(data), nil
}

func decodeJSONMap(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var value map[string]string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode map: %w", err)
	}
	if len(value) == 0 {
		return nil, nil
	}
	return value, nil
}

func encodeJSONList(value []string) (string, error) {
	if value == nil {
		value = []string{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode list: %w", err)
	}
	return string(data), nil
}

func decodeJSONList(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var value []string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	if len(value) == 0 {
		return nil, nil
	}
	return value, nil
}

// stampUpsert sets timestamps: CreatedAt on insert, UpdatedAt always.
func stampUpsert(server *ManagedServer) {
	if strings.TrimSpace(server.DisplayName) == "" {
		server.DisplayName = strings.TrimSpace(server.Name)
	}
	now := time.Now().UTC()
	if server.CreatedAt.IsZero() {
		server.CreatedAt = now
	}
	server.UpdatedAt = now
}

// collectManagedServers drains a row iterator into a slice.
func collectManagedServers(next func() (ManagedServer, bool, error), rowsErr func() error) ([]ManagedServer, error) {
	result := make([]ManagedServer, 0)
	for {
		server, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		result = append(result, server)
	}
	if err := rowsErr(); err != nil {
		return nil, fmt.Errorf("iterate mcp servers: %w", err)
	}
	return result, nil
}
