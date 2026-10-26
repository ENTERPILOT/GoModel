// Package sharedstore implements ext.SharedStore, the key/value store core
// hands to extensions, on the gateway's configured storage backend.
package sharedstore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SweepInterval is how often expired entries are deleted. Reads already hide
// them, so the sweep only bounds the table's size.
const SweepInterval = time.Hour

// backend is one storage engine's half of a Store. Times are unix seconds;
// an expiresAt of 0 never expires. Arguments are already validated.
type backend interface {
	put(ctx context.Context, namespace, key string, value []byte, expiresAt, now int64) error
	get(ctx context.Context, namespace, key string, now int64) ([]byte, bool, error)
	remove(ctx context.Context, namespace, key string) error
	list(ctx context.Context, namespace string, now int64) ([]ext.SharedEntry, error)
	deleteExpired(ctx context.Context, now int64) error
}

// Store validates calls, hides closed generations, and sweeps expired
// entries in front of a backend.
type Store struct {
	backend   backend
	now       func() time.Time
	closed    atomic.Bool
	stop      chan struct{}
	closeOnce sync.Once
}

var _ ext.SharedStore = (*Store)(nil)

// New opens the shared store on the gateway's storage connection and starts
// the expiry sweep. Close stops the sweep; the connection stays owned by the
// storage layer.
func New(ctx context.Context, shared storage.Storage) (*Store, error) {
	b, err := storage.ResolveSQLBackend[backend](ctx, shared,
		func(db sqlx.DB) (backend, error) { return newSQLBackend(ctx, db) },
		func(db *mongo.Database) (backend, error) { return newMongoBackend(ctx, db) },
	)
	if err != nil {
		return nil, fmt.Errorf("create extension shared store: %w", err)
	}
	store := newStore(b, time.Now)
	go storage.RunCleanupLoop(store.stop, SweepInterval, store.sweep)
	return store, nil
}

func newStore(b backend, now func() time.Time) *Store {
	return &Store{backend: b, now: now, stop: make(chan struct{})}
}

// Put stores value under key until expiresAt (zero: until deleted).
func (s *Store) Put(ctx context.Context, namespace, key string, value []byte, expiresAt time.Time) error {
	if err := s.check(namespace, key); err != nil {
		return err
	}
	if len(value) > ext.MaxSharedValueBytes {
		return fmt.Errorf("shared store value is %d bytes, over the %d byte limit", len(value), ext.MaxSharedValueBytes)
	}
	if value == nil {
		value = []byte{} // nil binds as SQL NULL
	}
	return s.backend.put(ctx, namespace, key, value, storage.UnixOrZero(expiresAt), s.now().Unix())
}

// Get returns the live value under key.
func (s *Store) Get(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	if err := s.check(namespace, key); err != nil {
		return nil, false, err
	}
	return s.backend.get(ctx, namespace, key, s.now().Unix())
}

// Delete removes key.
func (s *Store) Delete(ctx context.Context, namespace, key string) error {
	if err := s.check(namespace, key); err != nil {
		return err
	}
	return s.backend.remove(ctx, namespace, key)
}

// List returns every live entry of namespace.
func (s *Store) List(ctx context.Context, namespace string) ([]ext.SharedEntry, error) {
	if s.closed.Load() {
		return nil, ext.ErrSharedStoreClosed
	}
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	return s.backend.list(ctx, namespace, s.now().Unix())
}

// Close stops the sweep and fails every later call with
// ext.ErrSharedStoreClosed, so an extension still holding this generation's
// store never reaches a closed connection.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.stop)
	})
	return nil
}

func (s *Store) check(namespace, key string) error {
	if s.closed.Load() {
		return ext.ErrSharedStoreClosed
	}
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	return validateKey(key)
}

func (s *Store) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.backend.deleteExpired(ctx, s.now().Unix()); err != nil {
		slog.Warn("extension shared store sweep failed", "error", err)
	}
}

// nonNil reports an empty value as []byte{} on every backend, whichever way
// the driver decodes it.
func nonNil(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}
