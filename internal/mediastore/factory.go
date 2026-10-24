package mediastore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/blobstore"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// Blob storage types an operator can choose with MEDIA_STORAGE_TYPE.
const (
	StorageFilesystem = "filesystem"
	StorageMemory     = "memory"
)

// Result holds the initialized media service.
type Result struct {
	Service *Service
}

// Close releases the service and both stores behind it.
func (r *Result) Close() error {
	if r == nil || r.Service == nil {
		return nil
	}
	return r.Service.Close()
}

// New creates the media service: records on the shared storage connection,
// bytes in blobs.
func New(ctx context.Context, shared storage.Storage, blobs blobstore.Store) (*Result, error) {
	if shared == nil {
		return nil, errors.New("shared storage is required")
	}
	if blobs == nil {
		return nil, errors.New("blob store is required")
	}
	objects, err := storage.ResolveSQLBackend[Store](
		ctx,
		shared,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
	if err != nil {
		return nil, err
	}
	return &Result{Service: NewService(objects, blobs)}, nil
}

// OpenBlobStore builds the blob store named by storageType: a directory for
// "filesystem", process memory for "memory".
func OpenBlobStore(storageType, path string) (blobstore.Store, error) {
	switch strings.ToLower(strings.TrimSpace(storageType)) {
	case "", StorageFilesystem:
		return blobstore.NewFilesystem(path)
	case StorageMemory:
		return blobstore.NewMemory(), nil
	default:
		return nil, fmt.Errorf("unsupported media storage type %q (use %s or %s)", storageType, StorageFilesystem, StorageMemory)
	}
}
