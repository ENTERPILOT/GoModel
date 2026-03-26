package auditlog

import (
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/internal/storage"
)

// NewReader creates an audit log Reader from a storage backend.
// Returns nil when store is nil.
func NewReader(store storage.Storage) (Reader, error) {
	if store == nil {
		return nil, nil
	}

	return storage.ResolveBackend[Reader](
		store,
		func(db *sql.DB) (Reader, error) { return NewSQLiteReader(db) },
		func(pool *pgxpool.Pool) (Reader, error) { return NewPostgreSQLReader(pool) },
		func(db *mongo.Database) (Reader, error) { return NewMongoDBReader(db) },
	)
}
