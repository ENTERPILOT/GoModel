package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores managed MCP servers in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the mcp_servers table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS mcp_servers (
			name TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			transport TEXT NOT NULL DEFAULT 'http',
			headers JSONB NOT NULL DEFAULT '{}'::jsonb,
			description TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			allowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
			disallowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
			user_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
			tool_timeout_seconds INTEGER NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp_servers table: %w", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return nil, fmt.Errorf("add mcp_servers display_name: %w", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_servers SET display_name = name WHERE display_name = ''`); err != nil {
		return nil, fmt.Errorf("backfill mcp_servers display_name: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_updated_at ON mcp_servers(updated_at DESC)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return nil, fmt.Errorf("failed to create mcp_servers index: %w", err)
		}
	}
	return &PostgreSQLStore{pool: pool}, nil
}

const postgresSelectMCPServerColumns = `name, display_name, url, transport, headers, description, enabled, allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at`

func (s *PostgreSQLStore) List(ctx context.Context) ([]ManagedServer, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+postgresSelectMCPServerColumns+`
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
		server, err := scanPostgreSQLMCPServer(rows)
		return server, true, err
	}, rows.Err)
}

func (s *PostgreSQLStore) Get(ctx context.Context, name string) (*ManagedServer, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+postgresSelectMCPServerColumns+`
		FROM mcp_servers
		WHERE name = $1
	`, strings.TrimSpace(name))
	server, err := scanPostgreSQLMCPServer(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &server, nil
}

func (s *PostgreSQLStore) Upsert(ctx context.Context, server ManagedServer) error {
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
	_, err = s.pool.Exec(ctx, `
		INSERT INTO mcp_servers (
			name, display_name, url, transport, headers, description, enabled, allowed_tools, disallowed_tools, user_paths, tool_timeout_seconds, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12, $13)
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

func (s *PostgreSQLStore) Delete(ctx context.Context, name string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM mcp_servers WHERE name = $1`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func scanPostgreSQLMCPServer(scanner interface{ Scan(dest ...any) error }) (ManagedServer, error) {
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
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedServer{}, ErrNotFound
		}
		return ManagedServer{}, fmt.Errorf("scan mcp server: %w", err)
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
	server.CreatedAt = time.Unix(createdAt, 0).UTC()
	server.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return server, nil
}
