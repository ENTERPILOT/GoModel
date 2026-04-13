package modeloverrides

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/config"
	"gomodel/internal/storage"
)

// Result holds the initialized model override service and any owned resources.
type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the model override subsystem.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopRefresh != nil {
			r.stopRefresh()
			r.stopRefresh = nil
		}

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

// New creates a model override subsystem with its own storage connection.
func New(ctx context.Context, cfg *config.Config, catalog Catalog) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storeConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := newResult(ctx, cfg, storeConn, catalog)
	if err != nil {
		_ = storeConn.Close()
		return nil, err
	}
	result.Storage = storeConn
	return result, nil
}

// NewWithSharedStorage creates a model override subsystem using an existing storage connection.
func NewWithSharedStorage(ctx context.Context, cfg *config.Config, shared storage.Storage, catalog Catalog) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	return newResult(ctx, cfg, shared, catalog)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage, catalog Catalog) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, catalog, cfg.Models.EnabledByDefault)
	if err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}

	refreshInterval := time.Minute
	if cfg.Workflows.RefreshInterval > 0 {
		refreshInterval = cfg.Workflows.RefreshInterval
	}

	return &Result{
		Service:     service,
		Store:       store,
		stopRefresh: service.StartBackgroundRefresh(refreshInterval),
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
