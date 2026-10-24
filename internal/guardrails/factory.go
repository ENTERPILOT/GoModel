package guardrails

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
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
// building instances from the plugin catalog. Secret config values are
// sealed with box; a nil box stores them in plaintext. secrets resolves the
// secret references stored configs hold.
func New(ctx context.Context, shared storage.Storage, box *encryption.Box, refreshInterval time.Duration, catalog *plugins.Catalog, deps plugins.HostDeps, secrets *config.Secrets) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	rawStore, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	store := sealStore(rawStore, box, catalogSecretKeys(catalog))
	service, err := NewService(store, catalog, deps)
	if err != nil {
		return nil, err
	}
	service.secrets = secrets
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

// Reencrypt seals every guardrail secret that is in plaintext or sealed with
// a data key other than box's active one. The catalog identifies which config
// values are secret; a plaintext secret of a plugin missing from it is left
// as it is.
func Reencrypt(ctx context.Context, shared storage.Storage, box *encryption.Box, catalog *plugins.Catalog) (encryption.Report, error) {
	store, err := createStore(ctx, shared)
	if err != nil {
		return encryption.Report{Entity: "guardrail_definitions"}, err
	}
	defer func() { _ = store.Close() }()
	swap, ok := store.(configSwapper)
	if !ok {
		return encryption.Report{Entity: "guardrail_definitions"}, fmt.Errorf("guardrail store %T cannot re-encrypt", store)
	}
	return (&sealedStore{Store: store, box: box, secretKeys: catalogSecretKeys(catalog), swap: swap}).reencrypt(ctx, swap)
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
