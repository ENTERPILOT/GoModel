package usage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

const (
	usageInsertColumnCount = 23

	// Bind-parameter ceilings per statement: SQLite's default
	// SQLITE_MAX_VARIABLE_NUMBER and PostgreSQL's wire-protocol limit.
	sqliteMaxBindParameters   = 999
	postgresMaxBindParameters = 65535
)

// SQLStore implements UsageStore and PricingRecalculator for SQLite and
// PostgreSQL.
type SQLStore struct {
	db                     sqlx.DB
	dialect                usageDialect
	retentionDays          int
	recalculationBatchSize int
	stopCleanup            chan struct{}
	closeOnce              sync.Once
}

// usageTable is the usage table as both stores have always created it. The id
// column is TEXT on SQLite and UUID on PostgreSQL; it is a format verb because
// no portable type token covers UUID.
const usageTable = `
	CREATE TABLE IF NOT EXISTS usage (
		id %s PRIMARY KEY,
		request_id TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		timestamp ` + sqlx.TypeTimestamp + ` NOT NULL,
		model TEXT NOT NULL,
		provider TEXT NOT NULL,
		provider_name TEXT,
		endpoint TEXT NOT NULL,
		user_path TEXT,
		session_id TEXT,
		cache_type TEXT,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0,
		rewrite_cost_saved ` + sqlx.TypeFloat + `,
		raw_data ` + sqlx.TypeJSON + `
	)`

// usageMigrations add the columns introduced after the table's first release.
var usageMigrations = []string{
	"ALTER TABLE usage ADD COLUMN input_cost " + sqlx.TypeFloat,
	"ALTER TABLE usage ADD COLUMN output_cost " + sqlx.TypeFloat,
	"ALTER TABLE usage ADD COLUMN total_cost " + sqlx.TypeFloat,
	"ALTER TABLE usage ADD COLUMN cost_source TEXT DEFAULT ''",
	"ALTER TABLE usage ADD COLUMN costs_calculation_caveat TEXT DEFAULT ''",
	"ALTER TABLE usage ADD COLUMN provider_name TEXT",
	"ALTER TABLE usage ADD COLUMN user_path TEXT",
	"ALTER TABLE usage ADD COLUMN session_id TEXT",
	"ALTER TABLE usage ADD COLUMN cache_type TEXT",
	"ALTER TABLE usage ADD COLUMN labels " + sqlx.TypeJSON,
	"ALTER TABLE usage ADD COLUMN rewrite_tokens_saved INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE usage ADD COLUMN rewrite_cost_saved " + sqlx.TypeFloat,
}

var usageIndexes = []string{
	"CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage(timestamp)",
	"CREATE INDEX IF NOT EXISTS idx_usage_request_id ON usage(request_id)",
	"CREATE INDEX IF NOT EXISTS idx_usage_provider_id ON usage(provider_id)",
	"CREATE INDEX IF NOT EXISTS idx_usage_model ON usage(model)",
	"CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage(provider)",
	"CREATE INDEX IF NOT EXISTS idx_usage_provider_name ON usage(provider_name)",
	"CREATE INDEX IF NOT EXISTS idx_usage_user_path ON usage(user_path)",
	"CREATE INDEX IF NOT EXISTS idx_usage_session_id ON usage(session_id)",
	"CREATE INDEX IF NOT EXISTS idx_usage_user_path_normalized ON usage(COALESCE(NULLIF(TRIM(user_path), ''), '/'))",
	"CREATE INDEX IF NOT EXISTS idx_usage_cache_type ON usage(cache_type)",
}

// NewSQLStore creates the usage table and indexes if needed, and starts a
// background cleanup goroutine when retention is configured.
func NewSQLStore(ctx context.Context, db sqlx.DB, retentionDays int) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	dialect := usageDialectFor(db.Dialect())

	if err := db.Schema(ctx, fmt.Sprintf(usageTable, dialect.idType)); err != nil {
		return nil, fmt.Errorf("failed to create usage table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, usageMigrations...); err != nil {
		return nil, fmt.Errorf("failed to migrate usage table: %w", err)
	}
	for _, index := range append(append([]string(nil), usageIndexes...), dialect.indexes...) {
		if err := db.Schema(ctx, index); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &SQLStore{
		db:                     db,
		dialect:                dialect,
		retentionDays:          retentionDays,
		recalculationBatchSize: defaultRecalculationBatchSize,
		stopCleanup:            make(chan struct{}),
	}
	if retentionDays > 0 {
		go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	}
	return store, nil
}

// WriteBatch inserts usage entries, skipping ids that are already stored.
// Entries are chunked to stay within the engine's bind-parameter limit, and a
// batch spanning several chunks is written in one transaction.
func (s *SQLStore) WriteBatch(ctx context.Context, entries []*UsageEntry) error {
	if len(entries) == 0 {
		return nil
	}
	rowsPerQuery := s.dialect.maxBindParameters / usageInsertColumnCount
	if len(entries) <= rowsPerQuery {
		query, args := buildUsageInsert(s.db.Dialect(), entries)
		if _, err := s.db.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("failed to insert %d usage entries: %w", len(entries), err)
		}
		return nil
	}
	err := s.db.InTx(ctx, func(q sqlx.Querier) error {
		for start := 0; start < len(entries); start += rowsPerQuery {
			end := min(start+rowsPerQuery, len(entries))
			query, args := buildUsageInsert(s.db.Dialect(), entries[start:end])
			if _, err := q.Exec(ctx, query, args...); err != nil {
				return fmt.Errorf("batch chunk [%d:%d): %w", start, end, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to insert %d usage entries: %w", len(entries), err)
	}
	return nil
}

const usageInsertPrefix = `
		INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name,
			endpoint, user_path, session_id, cache_type, labels, input_tokens, output_tokens, total_tokens,
			rewrite_tokens_saved, rewrite_cost_saved, raw_data,
			input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat)
		VALUES `

const usageInsertSuffix = `
		ON CONFLICT (id) DO NOTHING
	`

// usageInsertRow is one VALUES tuple of usageInsertColumnCount placeholders.
var usageInsertRow = "(" + strings.TrimSuffix(strings.Repeat("?, ", usageInsertColumnCount), ", ") + ")"

func buildUsageInsert(dialect sqlx.Dialect, entries []*UsageEntry) (string, []any) {
	var builder strings.Builder
	builder.Grow(len(usageInsertPrefix) + len(usageInsertSuffix) + len(entries)*(len(usageInsertRow)+2))
	builder.WriteString(usageInsertPrefix)
	args := make([]any, 0, len(entries)*usageInsertColumnCount)

	for i, entry := range entries {
		entry = normalizedUsageEntryForStorage(entry)
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(usageInsertRow)

		var rawData any
		if rawDataJSON := marshalRawData(entry.RawData, entry.ID); rawDataJSON != nil {
			rawData = string(rawDataJSON)
		}
		args = append(args,
			entry.ID,
			entry.RequestID,
			entry.ProviderID,
			dialect.TimestampArg(entry.Timestamp),
			entry.Model,
			entry.Provider,
			entry.ProviderName,
			entry.Endpoint,
			entry.UserPath,
			entry.SessionID,
			cacheTypeValue(entry.CacheType),
			sqlutil.NullableJSONStrings(entry.Labels, entry.ID),
			entry.InputTokens,
			entry.OutputTokens,
			entry.TotalTokens,
			entry.RewriteTokensSaved,
			entry.RewriteCostSaved,
			rawData,
			entry.InputCost,
			entry.OutputCost,
			entry.TotalCost,
			entry.CostSource,
			entry.CostsCalculationCaveat,
		)
	}

	builder.WriteString(usageInsertSuffix)
	return builder.String(), args
}

// Flush is a no-op: writes are synchronous.
func (s *SQLStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine. The database is owned by the storage
// layer and stays open. Safe to call multiple times.
func (s *SQLStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes usage entries older than the retention period.
func (s *SQLStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)
	deleted, err := s.db.Exec(ctx, "DELETE FROM usage WHERE "+s.dialect.timeExpr+" < ?", s.dialect.timeArg(cutoff))
	if err != nil {
		slog.Error("failed to cleanup old usage entries", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("cleaned up old usage entries", "deleted", deleted)
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
