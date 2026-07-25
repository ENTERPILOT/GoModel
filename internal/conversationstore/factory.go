package conversationstore

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Result holds the initialized conversation store.
type Result struct {
	Store Store
}

// Close releases resources held by the conversation store.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Store != nil {
		if err := r.Store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store close: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %w", errors.Join(errs...))
	}
	return nil
}

// New creates a conversation store on the shared storage connection.
func New(ctx context.Context, shared storage.Storage) (*Result, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	conversationStore, err := createStore(ctx, shared)
	if err != nil {
		return nil, err
	}
	return &Result{Store: conversationStore}, nil
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
