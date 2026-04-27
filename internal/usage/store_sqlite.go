package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// SQLite has a default limit of 999 bindable parameters per query (SQLITE_MAX_VARIABLE_NUMBER).
// maxEntriesPerBatch derives from maxSQLiteParams / columnsPerUsageEntry.
const (
	maxSQLiteParams      = 999
	columnsPerUsageEntry = 18
	maxEntriesPerBatch   = maxSQLiteParams / columnsPerUsageEntry // 55 entries
)

// SQLiteStore implements UsageStore for SQLite databases.
type SQLiteStore struct {
	db            *sql.DB
	retentionDays int
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

// NewSQLiteStore creates a new SQLite usage store.
// It creates the usage table if it doesn't exist and starts
// a background cleanup goroutine if retention is configured.
func NewSQLiteStore(db *sql.DB, retentionDays int) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	// Create table for usage tracking
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage (
			id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			model TEXT NOT NULL,
			provider TEXT NOT NULL,
			provider_name TEXT,
			endpoint TEXT NOT NULL,
			user_path TEXT,
			cache_type TEXT,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			raw_data JSON
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create usage table: %w", err)
	}

	// Add cost columns (idempotent: SQLite lacks IF NOT EXISTS for ALTER TABLE ADD COLUMN)
	costMigrations := []string{
		"ALTER TABLE usage ADD COLUMN input_cost REAL",
		"ALTER TABLE usage ADD COLUMN output_cost REAL",
		"ALTER TABLE usage ADD COLUMN total_cost REAL",
		"ALTER TABLE usage ADD COLUMN costs_calculation_caveat TEXT DEFAULT ''",
		"ALTER TABLE usage ADD COLUMN provider_name TEXT",
		"ALTER TABLE usage ADD COLUMN user_path TEXT",
		"ALTER TABLE usage ADD COLUMN cache_type TEXT",
	}
	for _, migration := range costMigrations {
		if _, err := db.Exec(migration); err != nil {
			// "duplicate column name" means the column already exists — safe to ignore
			if !strings.Contains(err.Error(), "duplicate column") {
				return nil, fmt.Errorf("failed to run migration %q: %w", migration, err)
			}
		}
	}

	// Create indexes for common queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage(timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_usage_timestamp_epoch ON usage(unixepoch(REPLACE(timestamp, ' ', 'T')))",
		"CREATE INDEX IF NOT EXISTS idx_usage_request_id ON usage(request_id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_provider_id ON usage(provider_id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_model ON usage(model)",
		"CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage(provider)",
		"CREATE INDEX IF NOT EXISTS idx_usage_provider_name ON usage(provider_name)",
		"CREATE INDEX IF NOT EXISTS idx_usage_user_path ON usage(user_path)",
		"CREATE INDEX IF NOT EXISTS idx_usage_user_path_normalized ON usage(COALESCE(NULLIF(TRIM(user_path), ''), '/'))",
		"CREATE INDEX IF NOT EXISTS idx_usage_cache_type ON usage(cache_type)",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &SQLiteStore{
		db:            db,
		retentionDays: retentionDays,
		stopCleanup:   make(chan struct{}),
	}

	// Start background cleanup if retention is configured
	if retentionDays > 0 {
		go RunCleanupLoop(store.stopCleanup, store.cleanup)
	}

	return store, nil
}

// WriteBatch writes multiple usage entries to SQLite using batch insert.
// Entries are chunked to stay within SQLite's parameter limit.
func (s *SQLiteStore) WriteBatch(ctx context.Context, entries []*UsageEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Process entries in chunks to stay within SQLite's parameter limit
	for i := 0; i < len(entries); i += maxEntriesPerBatch {
		end := min(i+maxEntriesPerBatch, len(entries))
		chunk := entries[i:end]

		// Build batch insert query for this chunk
		placeholders := make([]string, len(chunk))
		values := make([]any, 0, len(chunk)*columnsPerUsageEntry)

		for j, e := range chunk {
			e = normalizedUsageEntryForStorage(e)
			placeholders[j] = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

			rawDataJSON := marshalRawData(e.RawData, e.ID)

			// Handle NULL for raw_data field
			var rawDataValue any
			if rawDataJSON != nil {
				rawDataValue = string(rawDataJSON)
			}

			values = append(values,
				e.ID,
				e.RequestID,
				e.ProviderID,
				e.Timestamp.UTC().Format(time.RFC3339Nano),
				e.Model,
				e.Provider,
				e.ProviderName,
				e.Endpoint,
				e.UserPath,
				cacheTypeValue(e.CacheType),
				e.InputTokens,
				e.OutputTokens,
				e.TotalTokens,
				rawDataValue,
				e.InputCost,
				e.OutputCost,
				e.TotalCost,
				e.CostsCalculationCaveat,
			)
		}

		query := `INSERT OR IGNORE INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name,
			endpoint, user_path, cache_type, input_tokens, output_tokens, total_tokens, raw_data,
			input_cost, output_cost, total_cost, costs_calculation_caveat) VALUES ` +
			strings.Join(placeholders, ",")

		_, err := s.db.ExecContext(ctx, query, values...)
		if err != nil {
			return fmt.Errorf("failed to insert usage batch %d: %w", i/maxEntriesPerBatch, err)
		}
	}

	return nil
}

// Flush is a no-op for SQLite as writes are synchronous.
func (s *SQLiteStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine.
// Note: We don't close the DB here as it's managed by the storage layer.
// Safe to call multiple times.
func (s *SQLiteStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes usage entries older than the retention period.
func (s *SQLiteStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays).UTC().Format(time.RFC3339Nano)

	result, err := s.db.Exec("DELETE FROM usage WHERE "+sqliteTimestampEpochExpr()+" < unixepoch(?)", cutoff)
	if err != nil {
		slog.Error("failed to cleanup old usage entries", "error", err)
		return
	}

	if rowsAffected, err := result.RowsAffected(); err == nil && rowsAffected > 0 {
		slog.Info("cleaned up old usage entries", "deleted", rowsAffected)
	}
}

// marshalRawData marshals raw_data to JSON for SQL storage.
// Returns nil if data is nil or empty, or "{}" if marshaling fails.
func marshalRawData(data map[string]any, entryID string) []byte {
	if len(data) == 0 {
		return nil
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal usage raw_data", "error", err, "id", entryID)
		return []byte("{}")
	}
	return dataJSON
}
