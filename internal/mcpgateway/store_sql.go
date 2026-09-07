package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores managed MCP servers in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlTable = `CREATE TABLE IF NOT EXISTS mcp_servers (
	name TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	transport TEXT NOT NULL DEFAULT 'http',
	headers TEXT NOT NULL DEFAULT '{}',
	description TEXT NOT NULL DEFAULT '',
	enabled ` + sqlx.TypeBool + ` NOT NULL DEFAULT TRUE,
	allowed_tools TEXT NOT NULL DEFAULT '[]',
	disallowed_tools TEXT NOT NULL DEFAULT '[]',
	user_paths TEXT NOT NULL DEFAULT '[]',
	tool_timeout_seconds INTEGER NOT NULL DEFAULT 0,
	created_at ` + sqlx.TypeInt64 + ` NOT NULL,
	updated_at ` + sqlx.TypeInt64 + ` NOT NULL
)`

var sqlIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled)`,
	`CREATE INDEX IF NOT EXISTS idx_mcp_servers_updated_at ON mcp_servers(updated_at DESC)`,
}

// sqlMigrations backfill columns added after the table's first release.
var sqlMigrations = []string{
	`ALTER TABLE mcp_servers ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`,
}

const selectMCPServerColumns = `name, display_name, url, transport, headers, description, enabled, ` +
	`allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at`

// NewSQLStore creates the mcp_servers table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTable); err != nil {
		return nil, fmt.Errorf("failed to create mcp_servers table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, err
	}
	// Rows predating display_name show the server name in the dashboard rather
	// than an empty label.
	if _, err := db.Exec(ctx, `UPDATE mcp_servers SET display_name = name WHERE display_name = ''`); err != nil {
		return nil, fmt.Errorf("backfill mcp_servers display_name: %w", err)
	}
	if err := db.Schema(ctx, sqlIndexes...); err != nil {
		return nil, fmt.Errorf("failed to create mcp_servers index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]ManagedServer, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+selectMCPServerColumns+`
		FROM mcp_servers
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer rows.Close()
	return collectManagedServers(func() (ManagedServer, bool, error) {
		if !rows.Next() {
			return ManagedServer{}, false, nil
		}
		server, err := scanSQLMCPServer(rows)
		return server, true, err
	}, rows.Err)
}

func (s *SQLStore) Get(ctx context.Context, name string) (*ManagedServer, error) {
	row := s.db.QueryRow(ctx, `
		SELECT `+selectMCPServerColumns+`
		FROM mcp_servers
		WHERE name = ?
	`, strings.TrimSpace(name))
	server, err := scanSQLMCPServer(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &server, nil
}

func (s *SQLStore) Upsert(ctx context.Context, server ManagedServer) error {
	stampUpsert(&server)
	headersJSON, err := encodeJSONMap(server.Headers)
	if err != nil {
		return err
	}
	allowedJSON, err := encodeJSONList(server.AllowedTools)
	if err != nil {
		return err
	}
	disallowedJSON, err := encodeJSONList(server.DisallowedTools)
	if err != nil {
		return err
	}
	pathsJSON, err := encodeJSONList(server.UserPaths)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO mcp_servers (
			name, display_name, url, transport, headers, description, enabled,
			allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			url = excluded.url,
			transport = excluded.transport,
			headers = excluded.headers,
			description = excluded.description,
			enabled = excluded.enabled,
			allowed_tools = excluded.allowed_tools,
			disallowed_tools = excluded.disallowed_tools,
			user_paths = excluded.user_paths,
			tool_timeout_seconds = excluded.tool_timeout_seconds,
			updated_at = excluded.updated_at
	`,
		strings.TrimSpace(server.Name),
		server.DisplayName,
		server.URL,
		server.Transport,
		headersJSON,
		server.Description,
		server.Enabled,
		allowedJSON,
		disallowedJSON,
		pathsJSON,
		server.ToolTimeoutSeconds,
		server.CreatedAt.Unix(),
		server.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert mcp server: %w", err)
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, name string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM mcp_servers WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func scanSQLMCPServer(scanner sqlx.Row) (ManagedServer, error) {
	var server ManagedServer
	var headers, allowed, disallowed, userPaths []byte
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&server.Name,
		&server.DisplayName,
		&server.URL,
		&server.Transport,
		&headers,
		&server.Description,
		&server.Enabled,
		&allowed,
		&disallowed,
		&userPaths,
		&server.ToolTimeoutSeconds,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ManagedServer{}, err
	}
	var err error
	if server.Headers, err = decodeJSONMap(headers); err != nil {
		return ManagedServer{}, err
	}
	if server.AllowedTools, err = decodeJSONList(allowed); err != nil {
		return ManagedServer{}, err
	}
	if server.DisallowedTools, err = decodeJSONList(disallowed); err != nil {
		return ManagedServer{}, err
	}
	if server.UserPaths, err = decodeJSONList(userPaths); err != nil {
		return ManagedServer{}, err
	}
	if server.DisplayName == "" {
		server.DisplayName = server.Name
	}
	server.CreatedAt = sqlutil.TimeFromUnix(createdAt)
	server.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
	return server, nil
}
