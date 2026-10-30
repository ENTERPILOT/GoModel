package ext

import (
	"context"
	"errors"
	"time"
)

// Limits of the shared store. A call that exceeds one fails without touching
// storage.
const (
	// MaxSharedNamespaceBytes bounds a namespace. A namespace is one or more
	// of a-z, 0-9, '.', '_' and '-'.
	MaxSharedNamespaceBytes = 64
	// MaxSharedKeyBytes bounds a key: non-empty valid UTF-8 without NUL bytes.
	MaxSharedKeyBytes = 256
	// MaxSharedValueBytes bounds a value.
	MaxSharedValueBytes = 64 << 10
)

// ErrSharedStoreClosed is returned by a shared store whose configuration
// generation has shut down. After a reload core binds the next generation's
// store; at process exit it is the last error an extension sees.
var ErrSharedStoreClosed = errors.New("extension shared store is closed")

// SharedEntry is one live entry of a shared store namespace.
type SharedEntry struct {
	Key   string
	Value []byte
	// ExpiresAt is the entry's expiry at second precision; zero means it
	// never expires.
	ExpiresAt time.Time
}

// SharedStore is a small key/value store that extensions share across
// gateway replicas. Core keeps it in the configured storage backend, so every
// replica on the same PostgreSQL or MongoDB database sees the same entries
// and they survive restarts. With SQLite it is durable but local to the
// instance that owns the file.
//
// It is meant for small coordination state (revocation lists, leases,
// markers), not bulk data: List reads a whole namespace, so keep namespaces
// small. Name namespaces after the extension and its purpose (for example
// "sso.revoked") so extensions do not collide.
//
// Entries are invisible once their expiry passes and are swept periodically.
// A Put replaces the entry; a Put with a past expiry therefore removes it.
// Calls go to the database: keep them off the hot request path or bound them
// with a context deadline. Implementations are safe for concurrent use.
type SharedStore interface {
	// Put stores value under key. Only the zero time.Time keeps the entry
	// until it is deleted; any other expiresAt that is not in the future,
	// the unix epoch included, removes the entry.
	Put(ctx context.Context, namespace, key string, value []byte, expiresAt time.Time) error
	// Get returns the live value under key, and whether there was one.
	Get(ctx context.Context, namespace, key string) ([]byte, bool, error)
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, namespace, key string) error
	// List returns every live entry of namespace, in no particular order.
	List(ctx context.Context, namespace string) ([]SharedEntry, error)
}

// SharedStoreAware is optionally implemented by a registered extension
// component: an authenticator, request rewriter, runtime setting, health
// checker, route selector, or proxy selector. Core calls SetSharedStore once
// the configuration generation that owns the store has built successfully,
// and again with the new store after every reload, so an implementation must
// accept repeated calls and always use the most recent store. Until the first
// call there is no store; extensions must keep working without one.
type SharedStoreAware interface {
	SetSharedStore(SharedStore)
}
