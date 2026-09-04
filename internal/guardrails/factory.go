package guardrails

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Result holds the initialized guardrail service and any owned resources.
type Result struct {
	Service       *Service
	Store         Store
	RefreshErrors <-chan error

	stopRefresh func()
	closeOnce   sync.Once
	closeErr    error
}

// Close releases resources held by the guardrails subsystem.
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
		if r.Service != nil {
			if err := r.Service.Close(context.Background()); err != nil {
				errs = append(errs, fmt.Errorf("instances close: %w", err))
			}
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

// New creates a guardrails subsystem using an existing storage connection,
// building instances from the plugin catalog.
func New(ctx context.Context, shared storage.Storage, refreshInterval time.Duration, catalog *plugins.Catalog, deps plugins.HostDeps) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	store, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, catalog, deps)
	if err != nil {
		return nil, err
	}
	if refreshInterval > 0 {
		// Workflows recompile on the same interval; two cycles later no
		// compiled workflow references a replaced instance any more.
		service.retireAfter = 2 * refreshInterval
	}
	if err := service.Refresh(ctx); err != nil {
		return nil, err
	}
	stopRefresh, refreshErrors := startGuardrailRefreshLoop(ctx, service, refreshInterval)
	return &Result{
		Service:       service,
		Store:         store,
		RefreshErrors: refreshErrors,
		stopRefresh:   stopRefresh,
	}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(ctx, db) },
	)
}

func startGuardrailRefreshLoop(parent context.Context, service *Service, interval time.Duration) (func(), <-chan error) {
	if parent == nil || service == nil {
		errs := make(chan error)
		close(errs)
		return func() {}, errs
	}
	if interval <= 0 {
		interval = time.Minute
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	errs := make(chan error, 1)
	var once sync.Once

	go func() {
		defer close(done)
		defer close(errs)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := service.Refresh(refreshCtx); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
				refreshCancel()
			}
		}
	}()

	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, errs
}
