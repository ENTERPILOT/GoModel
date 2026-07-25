package tagging

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

// Result bundles the tagging service with its store.
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

// ConfigRules converts declarative config.yaml / TAGGING_HEADER_* entries into
// managed tagging rules. Entries are already normalized by config.Load.
func ConfigRules(entries []config.TaggingHeaderConfig) []Rule {
	if len(entries) == 0 {
		return nil
	}
	rules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, Rule{
			Header:    entry.Header,
			Prefix:    entry.Prefix,
			DoNotPass: entry.DoNotPass,
			Delimiter: entry.Delimiter,
			Managed:   true,
		})
	}
	return rules
}

// New builds the tagging service on the shared storage connection
// and loads the persisted operator rules.
func New(ctx context.Context, cfg *config.Config, shared storage.Storage) (*Result, error) {
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
	service := NewService(ConfigRules(cfg.Tagging.Headers), store)
	if err := service.Refresh(ctx); err != nil {
		_ = store.Close()
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
