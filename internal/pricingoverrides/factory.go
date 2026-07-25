package pricingoverrides

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Result holds the initialized pricing override service and any owned resources.
type Result struct {
	Service *Service
	Store   Store

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the pricing override subsystem.
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
		if len(errs) > 0 {
			r.closeErr = fmt.Errorf("close errors: %w", errors.Join(errs...))
		}
	})
	return r.closeErr
}

// New creates a pricing override subsystem using an existing storage connection.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage, catalog Catalog, base usage.PricingResolver) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	return newResult(ctx, cfg, shared, catalog, base)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage, catalog Catalog, base usage.PricingResolver) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, catalog, base)
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
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
