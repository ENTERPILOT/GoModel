package batch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/config"
	"gomodel/internal/storage"
)

// Result holds the initialized batch store and optional owned storage.
type Result struct {
	Store   Store
	Storage storage.Storage
}

// Close releases resources held by the batch store.
func (r *Result) Close() error {
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
		return fmt.Errorf("close errors: %w", errors.Join(errs...))
	}
	return nil
}

// New creates a batch store from app configuration.
func New(ctx context.Context, cfg *config.Config) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storageCfg := cfg.Storage.BackendConfig()
	store, err := storage.New(ctx, storageCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	batchStore, err := createStore(ctx, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	return &Result{
		Store:   batchStore,
		Storage: store,
	}, nil
}

// NewWithSharedStorage creates a batch store using a shared storage connection.
func NewWithSharedStorage(ctx context.Context, shared storage.Storage) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	batchStore, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	return &Result{
		Store: batchStore,
	}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
