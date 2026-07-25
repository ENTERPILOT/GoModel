package budget

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

type Result struct {
	Service *Service
	Store   Store

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
		if len(errs) > 0 {
			r.closeErr = fmt.Errorf("close errors: %w", errors.Join(errs...))
		}
	})
	return r.closeErr
}

func New(ctx context.Context, cfg *config.Config, shared storage.Storage) (*Result, error) {
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
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, db) },
	)
}

func seedConfiguredBudgets(ctx context.Context, service *Service, cfg config.BudgetsConfig) error {
	if service == nil {
		return nil
	}
	budgets := make([]Budget, 0)
	for _, entry := range cfg.UserPaths {
		userPath, err := NormalizeUserPath(entry.Path)
		if err != nil {
			return fmt.Errorf("invalid budget user path %q: %w", entry.Path, err)
		}
		for limitIdx, limit := range entry.Limits {
			seconds := limit.PeriodSeconds
			if seconds <= 0 {
				parsed, ok := PeriodSeconds(limit.Period)
				if !ok {
					return fmt.Errorf("invalid budget period for user path %q limit %d: %q", userPath, limitIdx, limit.Period)
				}
				seconds = parsed
			}
			budgets = append(budgets, Budget{
				UserPath:      userPath,
				PeriodSeconds: seconds,
				Amount:        limit.Amount,
				Source:        SourceConfig,
			})
		}
	}
	return service.ReplaceConfigBudgets(ctx, budgets)
}
