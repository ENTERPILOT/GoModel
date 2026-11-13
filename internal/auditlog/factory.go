package auditlog

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
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
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if !cfg.Logging.Enabled {
		return &Result{Logger: &NoopLogger{}}, nil
	}
	if store == nil {
		return nil, fmt.Errorf("storage is required when audit logging is enabled")
	}

	logStore, err := createLogStore(ctx, store, cfg.Logging.RetentionDays)
	if err != nil {
		return nil, err
	}
	loggerCfg := buildLoggerConfig(cfg.Logging)
	loggerCfg.APIKeyHashSecret = loadAPIKeyHashSecret(ctx, store)
	return &Result{Logger: NewLogger(logStore, loggerCfg)}, nil
}

// apiKeyHashSecretKey is where the secret that keys LogData.APIKeyHash is
// kept: in the deployment's runtime_settings table, next to the install
// identifier.
const apiKeyHashSecretKey = "audit_api_key_hash_secret" //nolint:gosec // G101: settings key name, not a credential.

// loadAPIKeyHashSecret returns the deployment's secret, creating it on first
// use. The insert is atomic, so replicas sharing a database agree on one
// value and give the same key the same fingerprint. When the secret cannot be
// loaded the fingerprint is left out rather than computed without a key.
func loadAPIKeyHashSecret(ctx context.Context, store storage.Storage) string {
	settings, err := runtimesettings.NewStore(ctx, store)
	if err == nil {
		var secret string
		if secret, err = settings.SetDefault(ctx, apiKeyHashSecretKey, rand.Text()); err == nil {
			return secret
		}
	}
	slog.Warn("audit entries will not carry api_key_hash: its secret could not be loaded", "error", err)
	return ""
}

// createLogStore creates the appropriate LogStore for the given storage backend.
func createLogStore(ctx context.Context, store storage.Storage, retentionDays int) (LogStore, error) {
	return storage.ResolveSQLBackend[LogStore](
		ctx,
		store,
		func(db sqlx.DB) (LogStore, error) { return NewSQLStore(ctx, db, retentionDays) },
		func(db *mongo.Database) (LogStore, error) { return NewMongoDBStore(db, retentionDays) },
	)
}

// buildLoggerConfig creates an auditlog.Config from config.LogConfig.
func buildLoggerConfig(logCfg config.LogConfig) Config {
	imageScope := config.ResolveImageBodyScope(logCfg.LogImageBodiesScope)
	cfg := Config{
		Enabled:               logCfg.Enabled,
		LogBodies:             logCfg.LogBodies,
		LogAudioBodies:        logCfg.LogAudioBodies,
		LogEmbeddingBodies:    logCfg.LogEmbeddingBodies,
		LogImageInputs:        logCfg.LogImageBodies && imageScope.Inputs(),
		LogImageOutputs:       logCfg.LogImageBodies && imageScope.Outputs(),
		LogRevisionBodies:     logCfg.LogRevisionBodies,
		LogGuardrailSteps:     logCfg.LogGuardrailSteps,
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
