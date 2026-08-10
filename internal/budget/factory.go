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

func New(ctx context.Context, cfg *config.Config, shared storage.Storage, quotaTemplatesEnabled bool) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if !cfg.Budgets.Enabled {
		return &Result{}, nil
	}
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newResult(ctx, cfg, shared, quotaTemplatesEnabled)
}

func newResult(ctx context.Context, cfg *config.Config, storeConn storage.Storage, quotaTemplatesEnabled bool) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(ctx, store, WithQuotaTemplates(quotaTemplatesEnabled))
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
	budgets := make([]Budget, 0, len(cfg.UserPaths)+len(cfg.Labels))
	for _, entry := range cfg.UserPaths {
		seeded, err := seedBudgets(ScopeUserPath, entry.Path, entry.PerChild, entry.Limits)
		if err != nil {
			return err
		}
		budgets = append(budgets, seeded...)
	}
	for _, entry := range cfg.Labels {
		seeded, err := seedBudgets(ScopeLabel, entry.Label, false, entry.Limits)
		if err != nil {
			return err
		}
		budgets = append(budgets, seeded...)
	}
	return service.ReplaceConfigBudgets(ctx, budgets)
}

func seedBudgets(scope Scope, rawSubject string, perChild bool, limits []config.BudgetLimitConfig) ([]Budget, error) {
	subject, err := NormalizeSubject(scope, rawSubject)
	if err != nil {
		return nil, fmt.Errorf("invalid budget %s %q: %w", scope, rawSubject, err)
	}
	budgets := make([]Budget, 0, len(limits))
	for limitIdx, limit := range limits {
		seconds := limit.PeriodSeconds
		if seconds <= 0 {
			parsed, ok := PeriodSeconds(limit.Period)
			if !ok {
				return nil, fmt.Errorf("invalid budget period for %s %q limit %d: %q", scope, subject, limitIdx, limit.Period)
			}
			seconds = parsed
		}
		budgets = append(budgets, Budget{
			Scope:         scope,
			Subject:       subject,
			PerChild:      perChild || limit.PerChild,
			PeriodSeconds: seconds,
			Amount:        limit.Amount,
			Source:        SourceConfig,
		})
	}
	return budgets, nil
}
