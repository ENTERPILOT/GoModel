package users

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Result holds the initialized user policy service and its owned resources.
type Result struct {
	Service *Service
	Store   Store

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the users subsystem.
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
				r.closeErr = fmt.Errorf("close errors: %w", errors.Join(fmt.Errorf("store close: %w", err)))
			}
		}
	})
	return r.closeErr
}

// New creates the users subsystem on the shared storage connection. Declared
// config policies are validated against declaredProviders at startup.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage, catalog Catalog, declaredProviders []string) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	store, err := storage.ResolveSQLBackend[Store](
		ctx,
		shared,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, catalog)
	if err != nil {
		return nil, err
	}
	service.SetConfigUsers(ConfigUsers(cfg.Users))
	if err := service.ValidateManagedConfig(declaredProviders); err != nil {
		return nil, err
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	return &Result{
		Service:     service,
		Store:       store,
		stopRefresh: service.StartBackgroundRefresh(defaultRefreshInterval),
	}, nil
}

// ConfigUsers converts declarative config entries into managed policy rows.
func ConfigUsers(entries []config.UserConfig) []User {
	if len(entries) == 0 {
		return nil
	}
	result := make([]User, 0, len(entries))
	for _, entry := range entries {
		result = append(result, User{
			UserPath:      entry.Path,
			AllowedModels: entry.AllowedModels,
			Description:   entry.Description,
		})
	}
	return result
}
