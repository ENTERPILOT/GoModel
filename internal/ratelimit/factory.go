package ratelimit

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
		if r.Service != nil {
			r.Service.Close()
		}
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
	if !cfg.RateLimits.Enabled {
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
	service, err := NewService(ctx, store,
		WithQuotaTemplates(quotaTemplatesEnabled),
		WithFlushInterval(flushIntervalFromSeconds(cfg.RateLimits.FlushInterval)),
	)
	if err != nil {
		return nil, err
	}
	if err := seedConfiguredRules(ctx, service, cfg.RateLimits); err != nil {
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

func seedConfiguredRules(ctx context.Context, service *Service, cfg config.RateLimitsConfig) error {
	if service == nil {
		return nil
	}
	rules := make([]Rule, 0)
	appendRules := func(scope RuleScope, subject string, perChild bool, limits []config.RateLimitRuleConfig) error {
		normalized, err := NormalizeSubject(scope, subject)
		if err != nil {
			return fmt.Errorf("invalid rate limit %s subject %q: %w", scope, subject, err)
		}
		for limitIdx, limit := range limits {
			var seconds int64
			if limit.PeriodSeconds != nil {
				seconds = *limit.PeriodSeconds
			} else {
				parsed, ok := PeriodSecondsFromName(limit.Period)
				if !ok {
					return fmt.Errorf("invalid rate limit period for %s %q limit %d: %q", scope, normalized, limitIdx, limit.Period)
				}
				seconds = parsed
			}
			rules = append(rules, Rule{
				Scope:         scope,
				Subject:       normalized,
				PerChild:      perChild || limit.PerChild,
				PeriodSeconds: seconds,
				MaxRequests:   limit.MaxRequests,
				MaxTokens:     limit.MaxTokens,
				Source:        SourceConfig,
			})
		}
		return nil
	}
	for _, entry := range cfg.UserPaths {
		if err := appendRules(ScopeUserPath, entry.Path, entry.PerChild, entry.Limits); err != nil {
			return err
		}
	}
	for _, entry := range cfg.Providers {
		if err := appendRules(ScopeProvider, entry.Name, false, entry.Limits); err != nil {
			return err
		}
	}
	for _, entry := range cfg.Models {
		if err := appendRules(ScopeModel, entry.Model, false, entry.Limits); err != nil {
			return err
		}
	}
	return service.ReplaceConfigRules(ctx, rules)
}
