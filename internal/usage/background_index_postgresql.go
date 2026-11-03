package usage

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/internal/storage"
)

// backgroundIndexes are the PostgreSQL usage indexes added to a table that
// already holds history. They are built CONCURRENTLY off the startup path
// instead of in ensureUsageSchema, so a large table keeps accepting writes
// while they build; each is built once the previous one is valid.
var backgroundIndexes = []storage.BackgroundIndex{
	{
		// Serves the auth_key_id filter.
		Name:   "idx_usage_auth_key_id",
		Create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_auth_key_id ON usage(auth_key_id)`,
		Label:  "usage auth key",
	},
	{
		// Serves RecentWrites. Partial, so history written before the
		// write stamp existed stays out of it.
		Name:   writtenAtIndex,
		Create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS ` + writtenAtIndex + ` ON ` + writtenAtIndexDefinition,
		Label:  "usage written_at",
	},
}

// backgroundIndexLockKey serializes the usage builds across replicas (see
// storage.BackgroundIndexBuilder). Its value predates the shared builder, so
// replicas of an older release still take the same lock.
const backgroundIndexLockKey int64 = 0x676d2d75736b6579

func backgroundIndexBuilder(pool *pgxpool.Pool) storage.BackgroundIndexBuilder {
	return storage.BackgroundIndexBuilder{Pool: pool, LockKey: backgroundIndexLockKey}
}

// startBackgroundIndexBuild builds backgroundIndexes in the background until
// each is valid. The returned stop cancels the build and waits for it to
// return; it is safe to call more than once.
func startBackgroundIndexBuild(pool *pgxpool.Pool) (stop func()) {
	return backgroundIndexBuilder(pool).Start(backgroundIndexes...)
}
