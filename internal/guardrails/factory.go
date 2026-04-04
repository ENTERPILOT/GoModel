package guardrails

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

// Result holds the initialized guardrail service and any owned resources.
type Result struct {
	Service       *Service
	Store         Store
	Storage       storage.Storage
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

// New creates a guardrails subsystem with its own storage connection.
func New(ctx context.Context, cfg *config.Config, refreshInterval time.Duration, executors ...ChatCompletionExecutor) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	storeConn, err := storage.New(ctx, cfg.Storage.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	result, err := newResult(ctx, storeConn, refreshInterval, executors...)
	if err != nil {
		_ = storeConn.Close()
		return nil, err
	}
	result.Storage = storeConn
	return result, nil
}

// NewWithSharedStorage creates a guardrails subsystem using an existing storage connection.
func NewWithSharedStorage(ctx context.Context, shared storage.Storage, refreshInterval time.Duration, executors ...ChatCompletionExecutor) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newResult(ctx, shared, refreshInterval, executors...)
}

func newResult(ctx context.Context, storeConn storage.Storage, refreshInterval time.Duration, executors ...ChatCompletionExecutor) (*Result, error) {
	store, err := createStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, executors...)
	if err != nil {
		return nil, err
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
	return storage.ResolveBackend[Store](
		store,
		func(db *sql.DB) (Store, error) { return NewSQLiteStore(ctx, db) },
		func(pool *pgxpool.Pool) (Store, error) { return NewPostgreSQLStore(ctx, pool) },
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
