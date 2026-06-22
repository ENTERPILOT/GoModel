package virtualmodels

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

// Result holds the initialized virtual models service and any owned resources.
type Result struct {
	Service *Service
	Store   Store
	Storage storage.Storage

	stopAlias    func()
	stopOverride func()
	closeOnce    sync.Once
	closeErr     error
}

// Close releases resources held by the virtual models subsystem.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopAlias != nil {
			r.stopAlias()
			r.stopAlias = nil
		}
		if r.stopOverride != nil {
			r.stopOverride()
			r.stopOverride = nil
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

// New creates a virtual models subsystem with its own storage connection.
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

// NewWithSharedStorage creates a virtual models subsystem using an existing storage connection.
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
	if err := seedFromLegacy(ctx, store, storeConn); err != nil {
		return nil, fmt.Errorf("seed virtual models: %w", err)
	}

	service, err := NewService(store, catalog, cfg.Models.EnabledByDefault)
	if err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}

	// Mirror the legacy refresh cadences: aliases use the model cache interval,
	// access overrides use the workflows interval.
	aliasInterval := time.Duration(cfg.Cache.Model.RefreshInterval) * time.Second
	if aliasInterval <= 0 {
		aliasInterval = time.Hour
	}
	overrideInterval := time.Minute
	if cfg.Workflows.RefreshInterval > 0 {
		overrideInterval = cfg.Workflows.RefreshInterval
	}

	return &Result{
		Service:      service,
		Store:        store,
		stopAlias:    service.aliases.StartBackgroundRefresh(aliasInterval),
		stopOverride: service.overrides.StartBackgroundRefresh(overrideInterval),
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
