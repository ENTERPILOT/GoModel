package encryption

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Key is one row of the encryption_keys table: a data key wrapped by a
// key-encryption key. The plaintext data key is never stored.
type Key struct {
	// ID is what sealed values name. Ids are decimal sequence numbers: the
	// first key is "1", so instances starting together against an empty
	// database race on the primary key rather than creating two keys.
	ID string
	// Wrapped is the data key encrypted by the wrapper named in WrapperID.
	Wrapped   []byte
	WrapperID string
	// KDF, KDFParams and Salt describe how the local KEK is derived from
	// GOMODEL_ENCRYPTION_KEY. They are empty for an extension wrapper.
	KDF       string
	KDFParams string
	Salt      []byte
	// Active marks the key new values are sealed with.
	Active    bool
	CreatedAt time.Time
}

// KeyStore persists wrapped data keys.
type KeyStore interface {
	// List returns every key.
	List(ctx context.Context) ([]Key, error)
	// Insert adds a key and reports false when the id is already taken.
	Insert(ctx context.Context, key Key) (bool, error)
	// UpdateWrapping replaces how a key is wrapped (after a KEK rotation).
	UpdateWrapping(ctx context.Context, key Key) error
	// Activate makes id the active key.
	Activate(ctx context.Context, id string) error
}

// NewKeyStore opens the encryption_keys table or collection on the shared
// storage connection, creating it when needed.
func NewKeyStore(ctx context.Context, shared storage.Storage) (KeyStore, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	return storage.ResolveSQLBackend[KeyStore](
		ctx,
		shared,
		func(db sqlx.DB) (KeyStore, error) { return NewSQLKeyStore(ctx, db) },
		func(db *mongo.Database) (KeyStore, error) { return NewMongoDBKeyStore(ctx, db) },
	)
}
