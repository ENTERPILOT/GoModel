package auditlog

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// NewReader creates an audit log Reader from a storage backend.
// Returns nil when store is nil.
func NewReader(store storage.Storage) (Reader, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveSQLBackend[Reader](
		context.Background(),
		store,
		func(db sqlx.DB) (Reader, error) { return NewSQLReader(db) },
		func(db *mongo.Database) (Reader, error) { return NewMongoDBReader(db) },
	)
}
