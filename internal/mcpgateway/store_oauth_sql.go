package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlOAuthTable holds one sealed OAuth record per upstream server. It is keyed
// by server slug but is not tied to mcp_servers, so config-declared servers
// can be signed in too.
var sqlOAuthTable = `CREATE TABLE IF NOT EXISTS mcp_server_oauth (
	name TEXT PRIMARY KEY,
	data TEXT NOT NULL DEFAULT '',
	updated_at ` + sqlx.TypeInt64 + ` NOT NULL
)`

func (s *SQLStore) listOAuth(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name, data FROM mcp_server_oauth`)
	if err != nil {
		return nil, fmt.Errorf("list mcp oauth records: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var name, data string
		if err := rows.Scan(&name, &data); err != nil {
			return nil, fmt.Errorf("scan mcp oauth record: %w", err)
		}
		result[name] = data
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp oauth records: %w", err)
	}
	return result, nil
}

func (s *SQLStore) getOAuth(ctx context.Context, name string) (string, error) {
	var data string
	err := s.db.QueryRow(ctx, `SELECT data FROM mcp_server_oauth WHERE name = ?`, strings.TrimSpace(name)).Scan(&data)
	if errors.Is(err, sqlx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get mcp oauth record: %w", err)
	}
	return data, nil
}

func (s *SQLStore) putOAuth(ctx context.Context, name, data string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO mcp_server_oauth (name, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at
	`, strings.TrimSpace(name), data, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("save mcp oauth record: %w", err)
	}
	return nil
}

func (s *SQLStore) deleteOAuth(ctx context.Context, name string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM mcp_server_oauth WHERE name = ?`, strings.TrimSpace(name)); err != nil {
		return fmt.Errorf("delete mcp oauth record: %w", err)
	}
	return nil
}

// swapOAuth replaces a record only while it still holds current, so a token
// refresh or sign-in that landed meanwhile is never overwritten.
func (s *SQLStore) swapOAuth(ctx context.Context, name, current, next string) (bool, error) {
	affected, err := s.db.Exec(ctx, `UPDATE mcp_server_oauth SET data = ? WHERE name = ? AND data = ?`,
		next, strings.TrimSpace(name), current)
	if err != nil {
		return false, fmt.Errorf("swap mcp oauth record: %w", err)
	}
	return affected > 0, nil
}
