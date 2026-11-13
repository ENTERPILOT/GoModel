package storage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// BuildOnce gives each index that is not valid yet one build attempt, in
// order, and reports whether all of them are valid when it returns. Unlike
// Build it does not retry an attempt that failed. It does wait, checking every
// Retry, while another replica holds the lock: that replica may be building
// this very index, and if it stops before it gets there the index would
// otherwise be left unbuilt until the next start.
func (b BackgroundIndexBuilder) BuildOnce(ctx context.Context, indexes ...BackgroundIndex) bool {
	retry := b.Retry
	if retry <= 0 {
		retry = BackgroundIndexRetry
	}
	all := true
	for _, index := range indexes {
		outcome := b.attempt(ctx, index)
		for outcome == buildBusy {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(retry):
			}
			outcome = b.attempt(ctx, index)
		}
		all = all && outcome == buildValid
	}
	return all
}

// TryBuild makes one attempt and reports whether the index is valid when it
// returns. It does nothing but check when another session holds the lock.
func (b BackgroundIndexBuilder) TryBuild(ctx context.Context, index BackgroundIndex) bool {
	return b.attempt(ctx, index) == buildValid
}

// buildOutcome is how one attempt at an index ended.
type buildOutcome int

const (
	// buildValid: the index is valid.
	buildValid buildOutcome = iota
	// buildBusy: another session holds the lock, so nothing was attempted.
	buildBusy
	// buildFailed: this session's attempt failed, or it could not ask for
	// the lock.
	buildFailed
)

func (b BackgroundIndexBuilder) attempt(ctx context.Context, index BackgroundIndex) buildOutcome {
	if exists, valid, err := backgroundIndexState(ctx, b.Pool, index.Name); err == nil && exists && valid {
		return buildValid
	}
	outcome := buildFailed
	held, err := b.withLock(ctx, func(conn *pgxpool.Conn) {
		// Checked again under the lock: another replica may have finished
		// the build between the first check and taking the lock.
		exists, valid, err := backgroundIndexState(ctx, conn, index.Name)
		if err != nil {
			return
		}
		if exists && valid {
			outcome = buildValid
			return
		}
		if exists {
			slog.Warn("rebuilding invalid " + index.Label + " index")
			if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+index.Name); err != nil {
				logBackgroundIndexError(ctx, index, "drop invalid", err)
				return
			}
		}
		started := time.Now()
		if _, err := conn.Exec(ctx, index.Create); err != nil {
			logBackgroundIndexError(ctx, index, "create", err)
			return
		}
		if _, valid, err := backgroundIndexState(ctx, conn, index.Name); err != nil || !valid {
			return
		}
		slog.Info(index.Label+" index ready", "duration", time.Since(started).Round(time.Millisecond))
		outcome = buildValid
	})
	if err != nil {
		// Not busy: only a lock another session holds is worth waiting on.
		logBackgroundIndexError(ctx, index, "take the build lock for", err)
		return buildFailed
	}
	if !held {
		return buildBusy
	}
	return outcome
}

// TryDrop makes one attempt to retire an index the table no longer needs,
// and reports whether it is gone when it returns. The drop runs CONCURRENTLY
// and under the lock, so it never waits on another replica's build.
func (b BackgroundIndexBuilder) TryDrop(ctx context.Context, index BackgroundIndex) bool {
	if exists, _, err := backgroundIndexState(ctx, b.Pool, index.Name); err == nil && !exists {
		return true
	}
	dropped := false
	_, err := b.withLock(ctx, func(conn *pgxpool.Conn) {
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+index.Name); err != nil {
			logBackgroundIndexError(ctx, index, "drop", err)
			return
		}
		dropped = true
	})
	if err != nil {
		logBackgroundIndexError(ctx, index, "take the build lock to drop", err)
	}
	return dropped
}

// withLock runs fn on a connection holding the session advisory lock and
// reports whether it did. It returns false and no error when another session
// holds the lock, and an error when the lock could not be asked for at all.
func (b BackgroundIndexBuilder) withLock(ctx context.Context, fn func(conn *pgxpool.Conn)) (held bool, err error) {
	conn, err := b.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, b.LockKey).Scan(&held); err != nil || !held {
		return false, err
	}
	defer func() {
		// A pooled connection must not keep the session lock; drop it when
		// the unlock cannot be confirmed. Bounded so a broken connection
		// cannot stall shutdown, which waits for this goroutine.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, b.LockKey); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	fn(conn)
	return true, nil
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
