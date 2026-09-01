package virtualmodels

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Result holds the initialized virtual models service and any owned resources.
type Result struct {
	Service *Service
	Store   Store

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the virtual models subsystem.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopRefresh != nil {
			r.stopRefresh()
			r.stopRefresh = nil
		}
		if r.Store != nil {
			if err := r.Store.Close(); err != nil {
				r.closeErr = fmt.Errorf("store close: %w", err)
			}
		}
	})
	return r.closeErr
}

// New creates a virtual models subsystem using an existing storage connection.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage, catalog Catalog, declaredProviders []string) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	store, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	if err := rejectUnmigratedLegacyData(ctx, store, shared); err != nil {
		return nil, err
	}
	// The declarative virtual models are handed to the migration too: they are
	// overlaid on the store below, so a conversion must not commit a row that
	// only fails to load once they are.
	declared := ConfigModels(cfg.VirtualModels)
	if err := importLegacyFailoverRules(ctx, store, shared, cfg.Failover.Disabled, declared); err != nil {
		return nil, err
	}

	service, err := NewService(store, catalog, cfg.Models.EnabledByDefault)
	if err != nil {
		return nil, err
	}
	// Declarative virtual models (config.yaml / VIRTUAL_MODELS), plus the
	// deprecated failover rules translated into failover-strategy redirects, are
	// layered over the store as a managed overlay before the first refresh
	// builds the snapshot. A translated rule never overlays a stored virtual
	// model of the same source: the rule used to live beside it, and replacing
	// the stored redirect would silently change its routing.
	stored, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list virtual models: %w", err)
	}
	service.SetConfigModels(append(declared, FailoverConfigModels(cfg.Failover, declared, stored)...))
	if err := service.Refresh(ctx); err != nil {
		if IsValidationError(err) {
			// The named rows may predate today's validation (e.g. a chain cycle
			// an older failover-rule migration committed), and with the server
			// down the admin API cannot repair them — point at the store.
			return nil, fmt.Errorf("virtual models failed to load: %w; edit or delete the named entries (declared under virtual_models in config, or stored virtual_models entries in the database), then restart", err)
		}
		return nil, err
	}
	// Validate the managed redirects once, here at startup: an invalid declaration
	// (self-/cross-redirect target, or a misspelled target provider) fails loudly
	// rather than silently dropping. Background refreshes deliberately skip this
	// so a transient catalog gap cannot freeze the snapshot.
	if err := service.ValidateManagedConfig(declaredProviders); err != nil {
		return nil, err
	}

	// Virtual models are part of the model-config plane, so the unified store
	// refreshes on the model-cache cadence — the same interval the provider model
	// list uses. Cross-instance staleness is therefore identical to the model
	// cache's; operators tune CACHE_MODEL_REFRESH_INTERVAL for faster propagation.
	refreshInterval := time.Duration(cfg.Cache.Model.RefreshInterval) * time.Second
	if refreshInterval <= 0 {
		refreshInterval = time.Hour
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
