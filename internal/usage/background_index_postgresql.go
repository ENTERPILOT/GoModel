package usage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backgroundIndex is a PostgreSQL index added to a table that already holds
// usage history. It is built CONCURRENTLY off the startup path instead of in
// ensureUsageSchema, so a large table keeps accepting writes while it builds.
type backgroundIndex struct {
	name   string
	create string
	// label names the index in log messages.
	label string
}

// backgroundIndexes are built in order, each once the previous one is valid.
var backgroundIndexes = []backgroundIndex{
	{
		// Serves the auth_key_id filter.
		name:   "idx_usage_auth_key_id",
		create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_auth_key_id ON usage(auth_key_id)`,
		label:  "auth key",
	},
	{
		// Serves RecentWrites. Partial, so history written before the
		// write stamp existed stays out of it.
		name:   writtenAtIndex,
		create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS ` + writtenAtIndex + ` ON ` + writtenAtIndexDefinition,
		label:  "written_at",
	},
}

// backgroundIndexLockKey serializes the builds across replicas. It is a
// session lock taken without waiting: a blocking wait would hold a snapshot
// that the other replica's concurrent build has to wait out, deadlocking the
// two.
const backgroundIndexLockKey int64 = 0x676d2d75736b6579

// backgroundIndexRetry is how often a replica checks again while another one
// builds, or after a build failed.
const backgroundIndexRetry = time.Minute

// startBackgroundIndexBuild builds backgroundIndexes in the background until
// each is valid. The returned stop cancels the build and waits for it to
// return; it is safe to call more than once.
func startBackgroundIndexBuild(pool *pgxpool.Pool) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for _, index := range backgroundIndexes {
			for !tryBuildBackgroundIndex(ctx, pool, index) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backgroundIndexRetry):
				}
			}
		}
	})
	return func() {
		cancel()
		wg.Wait()
	}
}

// tryBuildBackgroundIndex reports whether the index is valid when it returns.
// Only the replica holding the advisory lock builds, so an invalid index it
// sees is a failed or abandoned build, never one in progress; it is dropped
// (CONCURRENTLY, so no exclusive lock queues in front of inserts) and rebuilt.
func tryBuildBackgroundIndex(ctx context.Context, pool *pgxpool.Pool, index backgroundIndex) bool {
	if exists, valid, err := backgroundIndexState(ctx, pool, index.name); err == nil && exists && valid {
		return true
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, backgroundIndexLockKey).Scan(&locked); err != nil || !locked {
		return false
	}
	defer func() {
		// A pooled connection must not keep the session lock; drop it when
		// the unlock cannot be confirmed. Bounded so a broken connection
		// cannot stall shutdown, which waits for this goroutine.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, backgroundIndexLockKey); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	exists, valid, err := backgroundIndexState(ctx, conn, index.name)
	if err != nil {
		return false
	}
	if exists && valid {
		return true
	}
	if exists {
		slog.Warn("usage: rebuilding invalid " + index.label + " index")
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+index.name); err != nil {
			logBackgroundIndexError(ctx, index, "drop invalid", err)
			return false
		}
	}
	started := time.Now()
	if _, err := conn.Exec(ctx, index.create); err != nil {
		logBackgroundIndexError(ctx, index, "create", err)
		return false
	}
	if _, valid, err := backgroundIndexState(ctx, conn, index.name); err != nil || !valid {
		return false
	}
	slog.Info("usage: "+index.label+" index ready", "duration", time.Since(started).Round(time.Millisecond))
	return true
}

type pgQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// backgroundIndexState reports whether the named index exists and is valid.
func backgroundIndexState(ctx context.Context, db pgQueryRower, name string) (exists, valid bool, err error) {
	err = db.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass($1)`, name).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, valid, err
}

func logBackgroundIndexError(ctx context.Context, index backgroundIndex, action string, err error) {
	if ctx.Err() == nil {
		slog.Warn("usage: failed to "+action+" "+index.label+" index", "error", err)
	}
}
