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

// authKeyIndex serves the auth_key_id filter. It is added to tables that
// already hold usage history, so it is built CONCURRENTLY off the startup
// path instead of in ensurePostgreSQLSchema: a large table keeps accepting
// writes while it builds.
const authKeyIndex = "idx_usage_auth_key_id"

// authKeyIndexLockKey serializes the build across replicas. It is a session
// lock taken without waiting: a blocking wait would hold a snapshot that the
// other replica's concurrent build has to wait out, deadlocking the two.
const authKeyIndexLockKey int64 = 0x676d2d75736b6579

// authKeyIndexRetry is how often a replica checks again while another one
// builds, or after a build failed.
const authKeyIndexRetry = time.Minute

// startAuthKeyIndexBuild builds authKeyIndex in the background until it is
// valid. The returned stop cancels the build and waits for it to return; it
// is safe to call more than once.
func startAuthKeyIndexBuild(pool *pgxpool.Pool) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for !tryBuildAuthKeyIndex(ctx, pool) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(authKeyIndexRetry):
			}
		}
	})
	return func() {
		cancel()
		wg.Wait()
	}
}

// tryBuildAuthKeyIndex reports whether the index is valid when it returns.
// Only the replica holding the advisory lock builds, so an invalid index it
// sees is a failed or abandoned build, never one in progress; it is dropped
// (CONCURRENTLY, so no exclusive lock queues in front of inserts) and rebuilt.
func tryBuildAuthKeyIndex(ctx context.Context, pool *pgxpool.Pool) bool {
	if exists, valid, err := authKeyIndexState(ctx, pool); err == nil && exists && valid {
		return true
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, authKeyIndexLockKey).Scan(&locked); err != nil || !locked {
		return false
	}
	defer func() {
		// A pooled connection must not keep the session lock; drop it when
		// the unlock cannot be confirmed. Bounded so a broken connection
		// cannot stall shutdown, which waits for this goroutine.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, authKeyIndexLockKey); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	exists, valid, err := authKeyIndexState(ctx, conn)
	if err != nil {
		return false
	}
	if exists && valid {
		return true
	}
	if exists {
		slog.Warn("usage: rebuilding invalid auth key index")
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS idx_usage_auth_key_id`); err != nil {
			logAuthKeyIndexError(ctx, "drop invalid", err)
			return false
		}
	}
	started := time.Now()
	if _, err := conn.Exec(ctx, `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_auth_key_id ON usage(auth_key_id)`); err != nil {
		logAuthKeyIndexError(ctx, "create", err)
		return false
	}
	if _, valid, err := authKeyIndexState(ctx, conn); err != nil || !valid {
		return false
	}
	slog.Info("usage: auth key index ready", "duration", time.Since(started).Round(time.Millisecond))
	return true
}

type pgQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// authKeyIndexState reports whether authKeyIndex exists and is valid.
func authKeyIndexState(ctx context.Context, db pgQueryRower) (exists, valid bool, err error) {
	err = db.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass($1)`, authKeyIndex).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, valid, err
}

func logAuthKeyIndexError(ctx context.Context, action string, err error) {
	if ctx.Err() == nil {
		slog.Warn("usage: failed to "+action+" auth key index", "error", err)
	}
}
