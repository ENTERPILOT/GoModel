package auditlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Result holds the initialized audit logger.
// The caller is responsible for calling Close() to release resources.
type Result struct {
	Logger LoggerInterface
}

// Close releases all resources held by the audit logger.
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

// New creates an audit logger on the shared storage connection.
// The caller must call Result.Close() during shutdown.
//
// If logging is disabled in the config, returns a NoopLogger and never
// touches storage.
func New(ctx context.Context, cfg *config.Config, store storage.Storage) (*Result, error) {
	if !cfg.Logging.Enabled {
		return &Result{Logger: &NoopLogger{}}, nil
	}
	if store == nil {
		return nil, fmt.Errorf("storage is required when audit logging is enabled")
	}

	logStore, err := createLogStore(store, cfg.Logging.RetentionDays)
	if err != nil {
		return nil, err
	}
	return &Result{Logger: NewLogger(logStore, buildLoggerConfig(cfg.Logging))}, nil
}

// createLogStore creates the appropriate LogStore for the given storage backend.
func createLogStore(store storage.Storage, retentionDays int) (LogStore, error) {
	return storage.ResolveBackend[LogStore](
		store,
		func(db *sql.DB) (LogStore, error) { return NewSQLiteStore(db, retentionDays) },
		func(pool *pgxpool.Pool) (LogStore, error) { return NewPostgreSQLStore(pool, retentionDays) },
		func(db *mongo.Database) (LogStore, error) { return NewMongoDBStore(db, retentionDays) },
	)
}

// buildLoggerConfig creates an auditlog.Config from config.LogConfig.
func buildLoggerConfig(logCfg config.LogConfig) Config {
	cfg := Config{
		Enabled:               logCfg.Enabled,
		LogBodies:             logCfg.LogBodies,
		LogAudioBodies:        logCfg.LogAudioBodies,
		LogHeaders:            logCfg.LogHeaders,
		BufferSize:            logCfg.BufferSize,
		FlushInterval:         time.Duration(logCfg.FlushInterval) * time.Second,
		RetentionDays:         logCfg.RetentionDays,
		OnlyModelInteractions: logCfg.OnlyModelInteractions,
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
