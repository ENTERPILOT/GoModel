package headerpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

// Result owns a header-policy service, its store, and optional storage connection.
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
			r.closeErr = errors.Join(errs...)
		}
	})
	return r.closeErr
}

// New creates the header-policy subsystem with an owned storage connection.
func New(ctx context.Context, cfg *config.Config, refreshInterval time.Duration) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storageConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("create header policy storage: %w", err)
	}
	result, err := newResult(ctx, storageConn, refreshInterval)
	if err != nil {
		_ = storageConn.Close()
		return nil, err
	}
	result.Storage = storageConn
	return result, nil
}

// NewWithSharedStorage creates the subsystem on an existing storage connection.
func NewWithSharedStorage(ctx context.Context, shared storage.Storage, refreshInterval time.Duration) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newResult(ctx, shared, refreshInterval)
}

func newResult(ctx context.Context, storageConn storage.Storage, refreshInterval time.Duration) (*Result, error) {
	store, err := createStore(ctx, storageConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store)
	if err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	return &Result{Service: service, Store: store, stopRefresh: service.StartBackgroundRefresh(ctx, refreshInterval)}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(ctx, db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, db) },
	)
}
