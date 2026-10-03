package usage

import (
	"context"
	"errors"
	"fmt"
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
}

// Close releases all resources held by the usage logger.
// Safe to call multiple times.
func (r *Result) Close() error {
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
		return &Result{
			Logger: NewNoopLogger(buildLoggerConfig(cfg.Usage)),
		}, nil
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

// createUsageStore creates the appropriate UsageStore for the given storage backend.
func createUsageStore(ctx context.Context, store storage.Storage, retentionDays int) (UsageStore, error) {
	return storage.ResolveSQLBackend[UsageStore](
		ctx,
		store,
		func(db sqlx.DB) (UsageStore, error) { return NewSQLStore(ctx, db, retentionDays) },
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
