package batch

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Result holds the initialized batch store.
type Result struct {
	Store Store
}

// Close releases resources held by the batch store.
func (r *Result) Close() error {
	if r.Store != nil {
		if err := r.Store.Close(); err != nil {
			return fmt.Errorf("store close: %w", err)
		}
	}
	return nil
}

// New creates a batch store on the shared storage connection.
func New(ctx context.Context, shared storage.Storage) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	batchStore, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	return &Result{
		Store: batchStore,
	}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
