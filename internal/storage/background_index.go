package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// BackgroundIndex is a PostgreSQL index added to a table that may already
// hold years of history. It is built CONCURRENTLY off the startup path, so
// the table keeps accepting writes while the build scans it.
type BackgroundIndex struct {
	// Name is the index name, as to_regclass resolves it.
	Name string
	// Create builds the index: CREATE INDEX CONCURRENTLY IF NOT EXISTS ...
	Create string
	// Label names the index in log messages, e.g. "usage auth key".
	Label string
}

// BackgroundIndexRetry is how often a builder checks again while another
// replica holds its lock, or after a build failed.
const BackgroundIndexRetry = time.Minute

// backgroundIndexLockPoll is how often WithLock checks again while another
// session holds the lock.
const backgroundIndexLockPoll = 100 * time.Millisecond

// BackgroundIndexBuilder builds BackgroundIndexes one at a time, and only
// while it holds a session advisory lock, so of all the replicas sharing a
// database one builds and the rest wait for it.
//
// The lock is taken without waiting. A blocking wait would hold a snapshot
// that the other replica's concurrent build has to wait out, deadlocking the
// two. It also means an invalid index the lock holder finds is a failed or
// abandoned build, never one in progress, so it is safe to drop and rebuild.
type BackgroundIndexBuilder struct {
	Pool *pgxpool.Pool
	// LockKey is the advisory lock key. Builders of one table share a key,
	// since PostgreSQL runs one concurrent build per table at a time.
	LockKey int64
	// Retry is the pause between attempts; BackgroundIndexRetry when zero.
	Retry time.Duration
}

// Start builds the indexes in the background until each is valid. The
// returned stop cancels the build and waits for it to return; it is safe to
// call more than once.
func (b BackgroundIndexBuilder) Start(indexes ...BackgroundIndex) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { b.Build(ctx, indexes...) })
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// Build tries each index that is not valid yet, one at a time and in order,
// and repeats every Retry until all of them are valid. An index that keeps
// failing does not hold up the ones after it. It reports whether all are
// valid, which is false only when ctx ends first.
func (b BackgroundIndexBuilder) Build(ctx context.Context, indexes ...BackgroundIndex) bool {
	retry := b.Retry
	if retry <= 0 {
		retry = BackgroundIndexRetry
	}
	pending := indexes
	for {
		var failed []BackgroundIndex
		for _, index := range pending {
			if ctx.Err() != nil {
				return false
			}
			if !b.TryBuild(ctx, index) {
				failed = append(failed, index)
			}
		}
		if len(failed) == 0 {
			return true
		}
		pending = failed
		select {
		case <-ctx.Done():
			return false
		case <-time.After(retry):
		}
	}
}

// TryBuild makes one attempt and reports whether the index is valid when it
// returns. It does nothing but check when another session holds the lock.
func (b BackgroundIndexBuilder) TryBuild(ctx context.Context, index BackgroundIndex) bool {
	if exists, valid, err := backgroundIndexState(ctx, b.Pool, index.Name); err == nil && exists && valid {
		return true
	}

	conn, err := b.Pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, b.LockKey).Scan(&locked); err != nil || !locked {
		return false
	}
	defer b.unlock(conn)

	// Checked again under the lock: another replica may have finished the
	// build between the first check and taking the lock.
	exists, valid, err := backgroundIndexState(ctx, conn, index.Name)
	if err != nil {
		return false
	}
	if exists && valid {
		return true
	}
	if exists {
		slog.Warn("rebuilding invalid " + index.Label + " index")
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+index.Name); err != nil {
			logBackgroundIndexError(ctx, index, "drop invalid", err)
			return false
		}
	}
	started := time.Now()
	if _, err := conn.Exec(ctx, index.Create); err != nil {
		logBackgroundIndexError(ctx, index, "create", err)
		return false
	}
	if _, valid, err := backgroundIndexState(ctx, conn, index.Name); err != nil || !valid {
		return false
	}
	slog.Info(index.Label+" index ready", "duration", time.Since(started).Round(time.Millisecond))
	return true
}

// WithLock waits for the builder's lock and runs fn on the session that holds
// it. It is for every other schema change to the builder's table, such as the
// migrations a store applies at startup: a statement that waits for the table
// while another replica builds an index on it holds a snapshot the build
// waits out, deadlocking the two. The wait for the lock polls, so it holds no
// snapshot either, and fn needs no second connection.
func (b BackgroundIndexBuilder) WithLock(ctx context.Context, fn func(sqlx.DB) error) error {
	conn, err := b.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire index build lock: %w", err)
	}
	defer conn.Release()
	for {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, b.LockKey).Scan(&locked); err != nil {
			return fmt.Errorf("acquire index build lock: %w", err)
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire index build lock: %w", ctx.Err())
		case <-time.After(backgroundIndexLockPoll):
		}
	}
	defer b.unlock(conn)

	db, err := sqlx.NewPostgreSQLConn(conn)
	if err != nil {
		return err
	}
	return fn(db)
}

// unlock returns the session lock. A pooled connection must not keep it, so
// the connection is dropped when the unlock cannot be confirmed. Bounded so a
// broken connection cannot stall shutdown, which waits for a build.
func (b BackgroundIndexBuilder) unlock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, b.LockKey); err != nil {
		_ = conn.Conn().Close(ctx)
	}
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

func logBackgroundIndexError(ctx context.Context, index BackgroundIndex, action string, err error) {
	if ctx.Err() == nil {
		slog.Warn("failed to "+action+" "+index.Label+" index", "error", err)
	}
}
