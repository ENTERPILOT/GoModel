package providers

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

// CredentialsResult holds the initialized provider-credentials subsystem and
// any resources it owns.
type CredentialsResult struct {
	Service *CredentialsService
	Store   CredentialStore

	closeOnce sync.Once
	closeErr  error
}

// Close releases resources held by the provider-credentials subsystem.
func (r *CredentialsResult) Close() error {
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

// NewCredentialsStore creates the provider-credentials
// subsystem using an existing storage connection.
func NewCredentialsStore(ctx context.Context, shared storage.Storage, factory *ProviderFactory, registry *ModelRegistry, declaredNames []string, resilience config.ResilienceConfig) (*CredentialsResult, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newCredentialsResult(ctx, shared, factory, registry, declaredNames, resilience)
}

func newCredentialsResult(ctx context.Context, storeConn storage.Storage, factory *ProviderFactory, registry *ModelRegistry, declaredNames []string, resilience config.ResilienceConfig) (*CredentialsResult, error) {
	store, err := createCredentialStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}

	service, err := NewCredentialsService(ctx, factory, registry, store, declaredNames, resilience)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	return &CredentialsResult{
		Service: service,
		Store:   store,
	}, nil
}

func createCredentialStore(ctx context.Context, store storage.Storage) (CredentialStore, error) {
	return storage.ResolveSQLBackend[CredentialStore](
		ctx,
		store,
		func(db sqlx.DB) (CredentialStore, error) { return NewSQLCredentialStore(ctx, db) },
		func(db *mongo.Database) (CredentialStore, error) { return NewMongoDBCredentialStore(db) },
	)
}
