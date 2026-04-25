package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/config"
	"gomodel/internal/storage"
)

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

func New(ctx context.Context, cfg *config.Config) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if !cfg.Budgets.Enabled {
		return &Result{}, nil
	}
	storeConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := newResult(ctx, cfg, storeConn)
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
	if !cfg.Budgets.Enabled {
		return &Result{}, nil
	}
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newResult(ctx, cfg, shared)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(ctx, store)
	if err != nil {
		return nil, err
	}
	if err := seedConfiguredBudgets(ctx, service, cfg.Budgets); err != nil {
		return nil, err
	}
	return &Result{Service: service, Store: store}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, db) },
	)
}

func seedConfiguredBudgets(ctx context.Context, service *Service, cfg config.BudgetsConfig) error {
	if service == nil || len(cfg.UserPaths) == 0 {
		return nil
	}
	budgets := make([]Budget, 0)
	for _, entry := range cfg.UserPaths {
		userPath, err := NormalizeUserPath(entry.Path)
		if err != nil {
			return fmt.Errorf("invalid budget user path %q: %w", entry.Path, err)
		}
		for _, limit := range entry.Limits {
			seconds := limit.PeriodSeconds
			if seconds <= 0 {
				if parsed, ok := PeriodSeconds(limit.Period); ok {
					seconds = parsed
				}
			}
			budgets = append(budgets, Budget{
				UserPath:      userPath,
				PeriodSeconds: seconds,
				Amount:        limit.Amount,
				Source:        "config",
			})
		}
	}
	return service.ReplaceConfigBudgets(ctx, budgets)
}
