package clustersync

import "context"

// Store persists one revision counter per entity in the shared database.
type Store interface {
	// Bump atomically increments the revision of entity, creating it at 1.
	Bump(ctx context.Context, entity string) error
	// Revisions returns the current revision of every entity bumped so far.
	Revisions(ctx context.Context) (map[string]int64, error)
}
