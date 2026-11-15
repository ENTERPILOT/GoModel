package auditlog

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// maxLastUsedKeysPerQuery bounds the number of bound parameters per last-used
// query. SQLite accepts at most 32,766 variables; 500 leaves headroom while
// keeping the per-batch aggregation narrow.
const maxLastUsedKeysPerQuery = 500

// GetLastUsedByAuthKeys returns the newest audit timestamp per auth key id.
// The query rides the idx_audit_auth_key_timestamp index; MAX over the timestamp
// column orders the same way the list queries' ORDER BY timestamp DESC does.
// Key ids are queried in bounded batches so key lists beyond the SQLite
// variable limit still resolve.
func (r *SQLReader) GetLastUsedByAuthKeys(ctx context.Context, keyIDs []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}

	for start := 0; start < len(keyIDs); start += maxLastUsedKeysPerQuery {
		end := min(start+maxLastUsedKeysPerQuery, len(keyIDs))
		batch, err := r.queryLastUsedBatch(ctx, keyIDs[start:end])
		if err != nil {
			return nil, err
		}
		maps.Copy(result, batch)
	}
	return result, nil
}

func (r *SQLReader) queryLastUsedBatch(ctx context.Context, keyIDs []string) (map[string]time.Time, error) {
	result := make(map[string]time.Time, len(keyIDs))

	args := make([]any, len(keyIDs))
	for i, id := range keyIDs {
		args[i] = id
	}
	rows, err := r.db.Query(ctx, lastUsedQuery(len(keyIDs)), args...)
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

// lastUsedQuery returns the per-key MAX(timestamp) query for n key ids.
func lastUsedQuery(n int) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return `SELECT auth_key_id, MAX(timestamp) FROM audit_logs
		WHERE auth_key_id IN (` + placeholders + `)
		GROUP BY auth_key_id`
}
