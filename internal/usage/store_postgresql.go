package usage

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	usageInsertColumnCount     = 16
	postgresMaxBindParameters  = 65535
	usageInsertMaxRowsPerQuery = postgresMaxBindParameters / usageInsertColumnCount
)

const usageInsertPrefix = `
		INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider,
			endpoint, user_path, input_tokens, output_tokens, total_tokens, raw_data,
			input_cost, output_cost, total_cost, costs_calculation_caveat)
		VALUES `

const usageInsertSuffix = `
		ON CONFLICT (id) DO NOTHING
	`

type usageBatchExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PostgreSQLStore implements UsageStore for PostgreSQL databases.
type PostgreSQLStore struct {
	pool          *pgxpool.Pool
	retentionDays int
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

// NewPostgreSQLStore creates a new PostgreSQL usage store.
// It creates the usage table if it doesn't exist and starts
// a background cleanup goroutine if retention is configured.
func NewPostgreSQLStore(pool *pgxpool.Pool, retentionDays int) (*PostgreSQLStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	ctx := context.Background()

	// Create table for usage tracking
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS usage (
			id UUID PRIMARY KEY,
			request_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL,
			model TEXT NOT NULL,
			provider TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			user_path TEXT,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			raw_data JSONB
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create usage table: %w", err)
	}

	// Add cost columns (idempotent via IF NOT EXISTS)
	costMigrations := []string{
		"ALTER TABLE usage ADD COLUMN IF NOT EXISTS input_cost DOUBLE PRECISION",
		"ALTER TABLE usage ADD COLUMN IF NOT EXISTS output_cost DOUBLE PRECISION",
		"ALTER TABLE usage ADD COLUMN IF NOT EXISTS total_cost DOUBLE PRECISION",
		"ALTER TABLE usage ADD COLUMN IF NOT EXISTS costs_calculation_caveat TEXT DEFAULT ''",
		"ALTER TABLE usage ADD COLUMN IF NOT EXISTS user_path TEXT",
	}
	for _, migration := range costMigrations {
		if _, err := pool.Exec(ctx, migration); err != nil {
			return nil, fmt.Errorf("failed to run migration: %w", err)
		}
	}

	// Create indexes for common queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage(timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_usage_request_id ON usage(request_id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_provider_id ON usage(provider_id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_model ON usage(model)",
		"CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage(provider)",
		"CREATE INDEX IF NOT EXISTS idx_usage_user_path ON usage(user_path)",
		"CREATE INDEX IF NOT EXISTS idx_usage_raw_data_gin ON usage USING GIN (raw_data)",
	}
	for _, idx := range indexes {
		if _, err := pool.Exec(ctx, idx); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &PostgreSQLStore{
		pool:          pool,
		retentionDays: retentionDays,
		stopCleanup:   make(chan struct{}),
	}

	// Start background cleanup if retention is configured
	if retentionDays > 0 {
		go RunCleanupLoop(store.stopCleanup, store.cleanup)
	}

	return store, nil
}

// WriteBatch writes multiple usage entries to PostgreSQL using batch insert.
func (s *PostgreSQLStore) WriteBatch(ctx context.Context, entries []*UsageEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// For larger batches, use a transaction to ensure atomicity
	// For smaller batches, use individual inserts without transaction overhead
	if len(entries) < 10 {
		return s.writeBatchSmall(ctx, entries)
	}

	return s.writeBatchLarge(ctx, entries)
}

// writeBatchSmall uses INSERT for small batches
func (s *PostgreSQLStore) writeBatchSmall(ctx context.Context, entries []*UsageEntry) error {
	if err := writeUsageInsertChunks(ctx, s.pool, entries); err != nil {
		slog.Warn("failed to insert usage batch", "error", err, "count", len(entries))
		return fmt.Errorf("failed to insert %d usage entries: %w", len(entries), err)
	}
	return nil
}

// writeBatchLarge uses batch insert for larger batches
func (s *PostgreSQLStore) writeBatchLarge(ctx context.Context, entries []*UsageEntry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := writeUsageInsertChunks(ctx, tx, entries); err != nil {
		slog.Warn("failed to insert usage batch in transaction", "error", err, "count", len(entries))
		return fmt.Errorf("failed to insert %d usage entries: %w", len(entries), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func writeUsageInsertChunks(ctx context.Context, exec usageBatchExecutor, entries []*UsageEntry) error {
	for start := 0; start < len(entries); start += usageInsertMaxRowsPerQuery {
		end := min(start+usageInsertMaxRowsPerQuery, len(entries))
		query, args := buildUsageInsert(entries[start:end])
		if _, err := exec.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("batch chunk [%d:%d): %w", start, end, err)
		}
	}
	return nil
}

func buildUsageInsert(entries []*UsageEntry) (string, []any) {
	var builder strings.Builder
	builder.Grow(len(usageInsertPrefix) + len(usageInsertSuffix) + len(entries)*usageInsertColumnCount*4)
	builder.WriteString(usageInsertPrefix)

	args := make([]any, 0, len(entries)*usageInsertColumnCount)
	placeholder := 1

	for i, entry := range entries {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteByte('(')
		for col := range usageInsertColumnCount {
			if col > 0 {
				builder.WriteString(", ")
			}
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(placeholder))
			placeholder++
		}
		builder.WriteByte(')')

		rawDataJSON := marshalRawData(entry.RawData, entry.ID)
		args = append(args,
			entry.ID,
			entry.RequestID,
			entry.ProviderID,
			entry.Timestamp,
			entry.Model,
			entry.Provider,
			entry.Endpoint,
			entry.UserPath,
			entry.InputTokens,
			entry.OutputTokens,
			entry.TotalTokens,
			rawDataJSON,
			entry.InputCost,
			entry.OutputCost,
			entry.TotalCost,
			entry.CostsCalculationCaveat,
		)
	}

	builder.WriteString(usageInsertSuffix)
	return builder.String(), args
}

// Flush is a no-op for PostgreSQL as writes are synchronous.
func (s *PostgreSQLStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine.
// Note: We don't close the pool here as it's managed by the storage layer.
// Safe to call multiple times.
func (s *PostgreSQLStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes usage entries older than the retention period.
func (s *PostgreSQLStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	result, err := s.pool.Exec(ctx, "DELETE FROM usage WHERE timestamp < $1", cutoff)
	if err != nil {
		slog.Error("failed to cleanup old usage entries", "error", err)
		return
	}

	if result.RowsAffected() > 0 {
		slog.Info("cleaned up old usage entries", "deleted", result.RowsAffected())
	}
}
