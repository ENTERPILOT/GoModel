package tagging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Result bundles the tagging service with its store and optional owned storage.
type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	closeOnce sync.Once
	closeErr  error
}

func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var errs []error
		if r.Store != nil {
			if err := r.Store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("store close: %w", err))
			}
		}
		if r.Storage != nil {
			if err := r.Storage.Close(); err != nil {
				errs = append(errs, fmt.Errorf("storage close: %w", err))
			}
		}
		if len(errs) > 0 {
			r.closeErr = fmt.Errorf("close errors: %w", errors.Join(errs...))
		}
	})
	return r.closeErr
}

// ConfigRules converts declarative config.yaml / TAGGING_HEADER_* entries into
// managed tagging rules. Entries are already normalized by config.Load.
func ConfigRules(entries []config.TaggingHeaderConfig) []Rule {
	if len(entries) == 0 {
		return nil
	}
	rules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, Rule{
			Header:    entry.Header,
			Prefix:    entry.Prefix,
			DoNotPass: entry.DoNotPass,
			Delimiter: entry.Delimiter,
			Managed:   true,
		})
	}
	return rules
}

// NewWithSharedStorage builds the tagging service on an existing storage
// backend and loads the persisted operator rules.
func NewWithSharedStorage(ctx context.Context, cfg *config.Config, shared storage.Storage) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	store, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	service := NewService(ConfigRules(cfg.Tagging.Headers), store)
	if err := service.Refresh(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return &Result{Service: service, Store: store}, nil
}

// New builds the tagging service with its own storage connection.
func New(ctx context.Context, cfg *config.Config) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storeConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := NewWithSharedStorage(ctx, cfg, storeConn)
	if err != nil {
		_ = storeConn.Close()
		return nil, err
	}
	result.Storage = storeConn
	return result, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, db) },
	)
}
