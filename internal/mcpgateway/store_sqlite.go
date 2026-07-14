package mcpgateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore stores managed MCP servers in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the mcp_servers table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS mcp_servers (
			name TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			transport TEXT NOT NULL DEFAULT 'http',
			headers TEXT NOT NULL DEFAULT '{}',
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			allowed_tools TEXT NOT NULL DEFAULT '[]',
			disallowed_tools TEXT NOT NULL DEFAULT '[]',
			user_paths TEXT NOT NULL DEFAULT '[]',
			tool_timeout_seconds INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp_servers table: %w", err)
	}
	if err := ensureSQLiteMCPDisplayName(db); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_updated_at ON mcp_servers(updated_at DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("failed to create mcp_servers index: %w", err)
		}
	}
	return &SQLiteStore{db: db}, nil
}

const sqliteSelectMCPServerColumns = `name, display_name, url, transport, headers, description, enabled, allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at`

func ensureSQLiteMCPDisplayName(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(mcp_servers)`)
	if err != nil {
		return fmt.Errorf("inspect mcp_servers schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("inspect mcp_servers column: %w", err)
		}
		if name == "display_name" {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect mcp_servers schema: %w", err)
	}
	if !found {
		if _, err := db.Exec(`ALTER TABLE mcp_servers ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add mcp_servers display_name: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE mcp_servers SET display_name = name WHERE display_name = ''`); err != nil {
		return fmt.Errorf("backfill mcp_servers display_name: %w", err)
	}
	return nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]ManagedServer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+sqliteSelectMCPServerColumns+`
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
		server, err := scanSQLiteMCPServer(rows)
		return server, true, err
	}, rows.Err)
}

func (s *SQLiteStore) Get(ctx context.Context, name string) (*ManagedServer, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+sqliteSelectMCPServerColumns+`
		FROM mcp_servers
		WHERE name = ?
	`, strings.TrimSpace(name))
	server, err := scanSQLiteMCPServer(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &server, nil
}

func (s *SQLiteStore) Upsert(ctx context.Context, server ManagedServer) error {
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
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO mcp_servers (
			name, display_name, url, transport, headers, description, enabled, allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at
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
		boolToSQLite(server.Enabled),
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

func (s *SQLiteStore) Delete(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return nil
}

func scanSQLiteMCPServer(scanner interface{ Scan(dest ...any) error }) (ManagedServer, error) {
	var server ManagedServer
	var headers, allowed, disallowed, userPaths string
	var enabled int
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&server.Name,
		&server.DisplayName,
		&server.URL,
		&server.Transport,
		&headers,
		&server.Description,
		&enabled,
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
	if server.Headers, err = decodeJSONMap([]byte(headers)); err != nil {
		return ManagedServer{}, err
	}
	if server.AllowedTools, err = decodeJSONList([]byte(allowed)); err != nil {
		return ManagedServer{}, err
	}
	if server.DisallowedTools, err = decodeJSONList([]byte(disallowed)); err != nil {
		return ManagedServer{}, err
	}
	if server.UserPaths, err = decodeJSONList([]byte(userPaths)); err != nil {
		return ManagedServer{}, err
	}
	server.Enabled = enabled != 0
	if server.DisplayName == "" {
		server.DisplayName = server.Name
	}
	server.CreatedAt = time.Unix(createdAt, 0).UTC()
	server.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return server, nil
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}
