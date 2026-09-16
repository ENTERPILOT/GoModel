package auditlog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// GetLastUsedByAuthKeys returns the newest audit timestamp per auth key id.
// The query rides the idx_audit_auth_key_id index; MAX over the timestamp
// column orders the same way the list queries' ORDER BY timestamp DESC does.
func (r *SQLReader) GetLastUsedByAuthKeys(ctx context.Context, keyIDs []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}

	args := make([]any, len(keyIDs))
	for i, id := range keyIDs {
		args[i] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keyIDs)), ",")

	rows, err := r.db.Query(ctx, `
		SELECT auth_key_id, MAX(timestamp) FROM audit_logs
		WHERE auth_key_id IN (`+placeholders+`)
		GROUP BY auth_key_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query auth key last used: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var timestamp sqlx.Timestamp
		if err := rows.Scan(&id, &timestamp); err != nil {
			return nil, fmt.Errorf("failed to scan auth key last used row: %w", err)
		}
		result[id] = timestamp.Time
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating auth key last used rows: %w", err)
	}
	return result, nil
}
