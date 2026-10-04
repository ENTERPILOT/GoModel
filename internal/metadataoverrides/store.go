package metadataoverrides

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// ErrNotFound indicates a requested metadata override was not found.
var ErrNotFound = errors.New("model metadata override not found")

// IsValidationError reports whether err is a validation error.
func IsValidationError(err error) bool {
	return modelselectors.IsValidationError(err)
}

// Store defines persistence operations for metadata overrides. Upsert
// receives normalized overrides and sets their timestamps.
type Store interface {
	List(ctx context.Context) ([]Override, error)
	Upsert(ctx context.Context, override Override) error
	Delete(ctx context.Context, selector string) error
	Close() error
}

func createStore(ctx context.Context, store storage.Storage) (Store, error) {
	return storage.ResolveSQLBackend[Store](
		ctx,
		store,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
}
