package requestlog

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// NewReader creates a request log Reader from a storage backend.
// Returns nil when store is nil.
func NewReader(store storage.Storage) (Reader, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveSQLBackend[Reader](
		context.Background(),
		store,
		func(db sqlx.DB) (Reader, error) { return newAdoptingSQLReader(db) },
		func(db *mongo.Database) (Reader, error) { return NewMongoDBReader(db) },
	)
}

// newAdoptingSQLReader creates a SQL reader on a database that may still hold
// the request log under its legacy table names. A reader does not create the
// tables, so one that finds none simply has nothing to read.
func newAdoptingSQLReader(db sqlx.DB) (*SQLReader, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := adoptLegacyStorage(context.Background(), db); err != nil {
		return nil, err
	}
	return NewSQLReader(db)
}
