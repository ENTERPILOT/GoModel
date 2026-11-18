package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sweepLockTimeout bounds connecting and taking the sweep lock, not the sweep
// it guards.
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
		return withSweepLock(ctx, pool.Config().ConnConfig, key, fn)
	}, sweep)
}

// withSweepLock holds the lock on a connection of its own, outside the pool,
// so the sweep's own statements never wait for the connection the lock
// holds, even with a pool of one. Closing the connection releases the lock
// on every path.
func withSweepLock(ctx context.Context, config *pgx.ConnConfig, key int64, fn func()) (bool, error) {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return false, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil || !locked {
		return false, err
	}
	fn()
	return true, nil
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
