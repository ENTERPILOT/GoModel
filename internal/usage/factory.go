package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
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
	usageStore, err := createUsageStore(ctx, store, cfg.Usage.RetentionDays)
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

	return storage.ResolveSQLBackend[UsageReader](
		context.Background(),
		store,
		func(db sqlx.DB) (UsageReader, error) { return NewSQLReader(db) },
		func(db *mongo.Database) (UsageReader, error) { return NewMongoDBReader(db) },
	)
}

// NewPricingRecalculator creates a PricingRecalculator from a storage backend.
// Returns nil if storage is nil.
func NewPricingRecalculator(store storage.Storage) (PricingRecalculator, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveSQLBackend[PricingRecalculator](
		context.Background(),
		store,
		func(db sqlx.DB) (PricingRecalculator, error) {
			return &SQLStore{db: db, dialect: usageDialectFor(db.Dialect())}, nil
		},
		func(db *mongo.Database) (PricingRecalculator, error) {
			if db == nil {
				return nil, fmt.Errorf("database is required")
			}
			return &MongoDBStore{collection: db.Collection("usage")}, nil
		},
	)
}

// ensureSchema brings the SQL usage table up to date without creating a
// store; MongoDB needs no migration. On PostgreSQL it also starts the
// background index build and returns its stop function.
func ensureSchema(ctx context.Context, store storage.Storage) (stop func(), err error) {
	return storage.ResolveSQLBackend[func()](
		ctx,
		store,
		func(db sqlx.DB) (func(), error) {
			if err := ensureUsageSchema(ctx, db); err != nil {
				return nil, err
			}
			return startIndexBuild(store), nil
		},
		func(*mongo.Database) (func(), error) { return nil, nil },
	)
}

// createUsageStore creates the appropriate UsageStore for the given storage backend.
func createUsageStore(ctx context.Context, store storage.Storage, retentionDays int) (UsageStore, error) {
	return storage.ResolveSQLBackend[UsageStore](
		ctx,
		store,
		func(db sqlx.DB) (UsageStore, error) {
			sqlStore, err := NewSQLStore(ctx, db, retentionDays)
			if err != nil {
				return nil, err
			}
			sqlStore.stopIndexBuild = startIndexBuild(store)
			return sqlStore, nil
		},
		func(db *mongo.Database) (UsageStore, error) { return NewMongoDBStore(db, retentionDays) },
	)
}

// startIndexBuild starts the background index build PostgreSQL needs and
// returns its stop function; other backends build every index inline.
func startIndexBuild(store storage.Storage) (stop func()) {
	if pg, ok := store.(storage.PostgreSQLStorage); ok {
		return startAuthKeyIndexBuild(pg.Pool())
	}
	return nil
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
