package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Result holds the initialized usage logger and its dependencies.
// The caller is responsible for calling Close() to release resources.
type Result struct {
	Logger LoggerInterface

	// stopIndexBuild stops the PostgreSQL index build started while usage
	// tracking is disabled; the store owns it otherwise.
	stopIndexBuild func()
}

// Close releases all resources held by the usage logger.
// Safe to call multiple times.
func (r *Result) Close() error {
	if r.stopIndexBuild != nil {
		r.stopIndexBuild()
	}
	var errs []error
	if r.Logger != nil {
		if err := r.Logger.Close(); err != nil {
			errs = append(errs, fmt.Errorf("logger close: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %w", errors.Join(errs...))
	}
	return nil
}

// New creates a usage logger on the shared storage connection.
// This is useful when you want to share the database connection with audit logging.
func New(ctx context.Context, cfg *config.Config, store storage.Storage) (*Result, error) {
	// Return noop if usage tracking is disabled
	if !cfg.Usage.Enabled {
		result := &Result{Logger: NewNoopLogger(buildLoggerConfig(cfg.Usage))}
		// The usage reader still serves history recorded while tracking was
		// on, so keep the schema current even though nothing new is written.
		if store != nil {
			stop, err := ensureSchema(ctx, store)
			if err != nil {
				slog.Warn("usage schema update skipped while usage tracking is disabled", "error", err)
			}
			result.stopIndexBuild = stop
		}
		return result, nil
	}

	if store == nil {
		return nil, fmt.Errorf("storage is required when usage tracking is enabled")
	}

	// Create the usage store based on storage type
	usageStore, err := createUsageStore(store, cfg.Usage.RetentionDays)
	if err != nil {
		return nil, err
	}

	// Create logger configuration
	logCfg := buildLoggerConfig(cfg.Usage)

	return &Result{Logger: NewLogger(usageStore, logCfg)}, nil
}

// NewReader creates a UsageReader from a storage backend.
// Returns nil if the storage is nil (usage data not available).
func NewReader(store storage.Storage) (UsageReader, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveBackend[UsageReader](
		store,
		func(db *sql.DB) (UsageReader, error) { return NewSQLiteReader(db) },
		func(pool *pgxpool.Pool) (UsageReader, error) { return NewPostgreSQLReader(pool) },
		func(db *mongo.Database) (UsageReader, error) { return NewMongoDBReader(db) },
	)
}

// NewPricingRecalculator creates a PricingRecalculator from a storage backend.
// Returns nil if storage is nil.
func NewPricingRecalculator(store storage.Storage) (PricingRecalculator, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveBackend[PricingRecalculator](
		store,
		func(db *sql.DB) (PricingRecalculator, error) {
			if db == nil {
				return nil, fmt.Errorf("database connection is required")
			}
			return &SQLiteStore{db: db}, nil
		},
		func(pool *pgxpool.Pool) (PricingRecalculator, error) {
			if pool == nil {
				return nil, fmt.Errorf("connection pool is required")
			}
			return &PostgreSQLStore{pool: pool}, nil
		},
		func(db *mongo.Database) (PricingRecalculator, error) {
			if db == nil {
				return nil, fmt.Errorf("database is required")
			}
			return &MongoDBStore{collection: db.Collection("usage")}, nil
		},
	)
}

// createUsageStore creates the appropriate UsageStore for the given storage backend.
// ensureSchema brings the SQL usage table up to date without creating a
// store; MongoDB needs no migration. On PostgreSQL it also starts the
// background index build and returns its stop function.
func ensureSchema(ctx context.Context, store storage.Storage) (stop func(), err error) {
	return storage.ResolveBackend[func()](
		store,
		func(db *sql.DB) (func(), error) { return nil, ensureSQLiteSchema(db) },
		func(pool *pgxpool.Pool) (func(), error) {
			if err := ensurePostgreSQLSchema(ctx, pool); err != nil {
				return nil, err
			}
			return startAuthKeyIndexBuild(pool), nil
		},
		func(*mongo.Database) (func(), error) { return nil, nil },
	)
}

func createUsageStore(store storage.Storage, retentionDays int) (UsageStore, error) {
	return storage.ResolveBackend[UsageStore](
		store,
		func(db *sql.DB) (UsageStore, error) { return NewSQLiteStore(db, retentionDays) },
		func(pool *pgxpool.Pool) (UsageStore, error) { return NewPostgreSQLStore(pool, retentionDays) },
		func(db *mongo.Database) (UsageStore, error) { return NewMongoDBStore(db, retentionDays) },
	)
}

// buildLoggerConfig creates a usage.Config from config.UsageConfig.
func buildLoggerConfig(usageCfg config.UsageConfig) Config {
	cfg := Config{
		Enabled:                   usageCfg.Enabled,
		EnforceReturningUsageData: usageCfg.EnforceReturningUsageData,
		BufferSize:                usageCfg.BufferSize,
		FlushInterval:             time.Duration(usageCfg.FlushInterval) * time.Second,
		RetentionDays:             usageCfg.RetentionDays,
	}

	// Apply defaults
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1000
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}

	return cfg
}
