package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
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
// subsystem using an existing storage connection. Secret fields are sealed
// with box; a nil box stores them in plaintext.
func NewCredentialsStore(ctx context.Context, shared storage.Storage, box *encryption.Box, factory *ProviderFactory, registry *ModelRegistry, declaredNames []string, resilience config.ResilienceConfig) (*CredentialsResult, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return newCredentialsResult(ctx, shared, box, factory, registry, declaredNames, resilience)
}

// ReencryptCredentials seals every provider credential secret that is in
// plaintext or sealed with a data key other than box's active one.
func ReencryptCredentials(ctx context.Context, shared storage.Storage, box *encryption.Box) (encryption.Report, error) {
	store, err := createCredentialStore(ctx, shared)
	if err != nil {
		return encryption.Report{Entity: "provider_credentials"}, err
	}
	defer func() { _ = store.Close() }()
	return (&sealedCredentialStore{CredentialStore: store, box: box}).reencrypt(ctx)
}

func newCredentialsResult(ctx context.Context, storeConn storage.Storage, box *encryption.Box, factory *ProviderFactory, registry *ModelRegistry, declaredNames []string, resilience config.ResilienceConfig) (*CredentialsResult, error) {
	rawStore, err := createCredentialStore(ctx, storeConn)
	if err != nil {
		return nil, err
	}
	store := sealCredentialStore(rawStore, box)

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
