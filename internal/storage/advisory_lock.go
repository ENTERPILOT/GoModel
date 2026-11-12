package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// withSessionAdvisoryLock runs fn on a connection of its own while that
// session holds the PostgreSQL advisory lock key, and reports whether it took
// the lock. It does not wait: when another session holds the key it returns
// false without running fn. The lock is released when fn returns, on every
// path.
func withSessionAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64, fn func(conn *pgxpool.Conn)) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil || !locked {
		return false, err
	}
	defer func() {
		// A pooled connection must not keep the session lock; drop it when
		// the unlock cannot be confirmed. Bounded so a broken connection
		// cannot stall shutdown, which waits for the lock holder.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	fn(conn)
	return true, nil
}
