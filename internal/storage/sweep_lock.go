package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sweepLockTimeout bounds taking the sweep lock, not the sweep it guards.
const sweepLockTimeout = 30 * time.Second

// SingleReplicaSweep makes sweep run on one replica at a time when the
// database is PostgreSQL: each round it runs only while this replica holds
// the session advisory lock key, and a round in which another replica holds
// it is skipped, since that replica is already deleting the same rows. Use a
// key per table. With a nil pool (SQLite, a single node) it returns sweep
// unchanged.
func SingleReplicaSweep(pool *pgxpool.Pool, key int64, sweep func()) func() {
	if pool == nil {
		return sweep
	}
	return lockedSweep(func(ctx context.Context, fn func()) (bool, error) {
		return withSessionAdvisoryLock(ctx, pool, key, func(*pgxpool.Conn) { fn() })
	}, sweep)
}

// lockedSweep runs sweep through tryLock, which runs it only when it takes
// the lock and reports whether it did.
func lockedSweep(tryLock func(ctx context.Context, fn func()) (bool, error), sweep func()) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), sweepLockTimeout)
		defer cancel()
		ran, err := tryLock(ctx, sweep)
		switch {
		case err != nil:
			slog.Warn("skipped retention sweep: failed to take its lock", "error", err)
		case !ran:
			slog.Debug("skipped retention sweep: another replica is running it")
		}
	}
}
