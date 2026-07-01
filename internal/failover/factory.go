package failover

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

type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

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
	service, err := NewService(store, cfg.Failover)
	if err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	interval := refreshInterval(cfg)
	return &Result{
		Service:     service,
		Store:       store,
		stopRefresh: service.StartBackgroundRefresh(interval),
	}, nil
}

func refreshInterval(cfg *config.Config) time.Duration {
	// Failover mappings are runtime routing policy, so they refresh on the same
	// cadence as workflows and pricing overrides rather than the model cache.
	if cfg != nil && cfg.Workflows.RefreshInterval > 0 {
		return cfg.Workflows.RefreshInterval
	}
	return time.Minute
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
