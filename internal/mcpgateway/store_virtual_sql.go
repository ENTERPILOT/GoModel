package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

var sqlVirtualTable = `CREATE TABLE IF NOT EXISTS mcp_virtual_servers (
	name TEXT PRIMARY KEY,
	description TEXT NOT NULL DEFAULT '',
	servers TEXT NOT NULL DEFAULT '[]',
	tool_discovery TEXT NOT NULL DEFAULT '',
	created_at ` + sqlx.TypeInt64 + ` NOT NULL,
	updated_at ` + sqlx.TypeInt64 + ` NOT NULL
)`

const selectMCPVirtualColumns = `name, description, servers, tool_discovery, created_at, updated_at`

func (s *SQLStore) ListVirtual(ctx context.Context) ([]ManagedVirtualServer, error) {
	rows, err := s.db.Query(ctx, `SELECT `+selectMCPVirtualColumns+` FROM mcp_virtual_servers ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list mcp virtual servers: %w", err)
	}
	defer rows.Close()
	result := make([]ManagedVirtualServer, 0)
	for rows.Next() {
		virtual, err := scanSQLMCPVirtual(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp virtual server: %w", err)
		}
		result = append(result, virtual)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp virtual servers: %w", err)
	}
	return result, nil
}

func (s *SQLStore) GetVirtual(ctx context.Context, name string) (*ManagedVirtualServer, error) {
	row := s.db.QueryRow(ctx, `SELECT `+selectMCPVirtualColumns+` FROM mcp_virtual_servers WHERE name = ?`, strings.TrimSpace(name))
	virtual, err := scanSQLMCPVirtual(row)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get mcp virtual server: %w", err)
	}
	return &virtual, nil
}

func (s *SQLStore) UpsertVirtual(ctx context.Context, virtual ManagedVirtualServer) error {
	stampVirtualUpsert(&virtual)
	servers, err := encodeJSONList(virtual.Servers)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO mcp_virtual_servers (name, description, servers, tool_discovery, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description,
			servers = excluded.servers,
			tool_discovery = excluded.tool_discovery,
			updated_at = excluded.updated_at
	`,
		strings.TrimSpace(virtual.Name),
		virtual.Description,
		servers,
		virtual.ToolDiscovery,
		virtual.CreatedAt.Unix(),
		virtual.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert mcp virtual server: %w", err)
	}
	return nil
}

func (s *SQLStore) UpdateVirtual(ctx context.Context, virtual ManagedVirtualServer) error {
	stampVirtualUpsert(&virtual)
	servers, err := encodeJSONList(virtual.Servers)
	if err != nil {
		return err
	}
	affected, err := s.db.Exec(ctx, `
		UPDATE mcp_virtual_servers SET description = ?, servers = ?, tool_discovery = ?, updated_at = ?
		WHERE name = ?
	`,
		virtual.Description,
		servers,
		virtual.ToolDiscovery,
		virtual.UpdatedAt.Unix(),
		strings.TrimSpace(virtual.Name),
	)
	if err != nil {
		return fmt.Errorf("update mcp virtual server: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteVirtual(ctx context.Context, name string) error {
	affected, err := s.db.Exec(ctx, `DELETE FROM mcp_virtual_servers WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("delete mcp virtual server: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSQLMCPVirtual(scanner sqlx.Row) (ManagedVirtualServer, error) {
	var virtual ManagedVirtualServer
	var servers []byte
	var createdAt, updatedAt int64
	if err := scanner.Scan(&virtual.Name, &virtual.Description, &servers, &virtual.ToolDiscovery, &createdAt, &updatedAt); err != nil {
		return ManagedVirtualServer{}, err
	}
	var err error
	if virtual.Servers, err = decodeJSONList(servers); err != nil {
		return ManagedVirtualServer{}, err
	}
	virtual.CreatedAt = time.Unix(createdAt, 0).UTC()
	virtual.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return virtual, nil
}
