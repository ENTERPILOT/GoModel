package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// testIndexLockKey is this test's own lock, so it never contends with the
// builders other packages' tests run against the same server.
const testIndexLockKey int64 = 0x746573742d696478

var testIndex = BackgroundIndex{
	Name:   "idx_background_items_value",
	Create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_background_items_value ON background_items(value)`,
	Label:  "test",
}

func newBackgroundIndexPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return nil // skipped: no test server configured
	}
	_, err := pool.Exec(context.Background(), `CREATE TABLE background_items (id BIGINT PRIMARY KEY, value TEXT)`)
	require.NoError(t, err)
	return pool
}

func indexState(t *testing.T, pool *pgxpool.Pool) (exists, valid bool) {
	t.Helper()
	exists, valid, err := backgroundIndexState(context.Background(), pool, testIndex.Name)
	require.NoError(t, err)
	return exists, valid
}

// holdLock takes the builder's lock on a session of its own, as another
// replica would, and returns its release.
func holdLock(t *testing.T, pool *pgxpool.Pool) (release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, testIndexLockKey)
	require.NoError(t, err)
	// Once, because a timer and the test cleanup can both release, and the
	// cleanup must wait for a release the timer started. The error is
	// checked in the cleanup, on the test goroutine.
	var (
		once      sync.Once
		unlockErr error
	)
	release = func() {
		once.Do(func() {
			defer conn.Release()
			_, unlockErr = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, testIndexLockKey)
		})
	}
	t.Cleanup(func() {
		release()
		require.NoError(t, unlockErr)
	})
	return release
}

// leaveInvalidIndex creates the index name as an invalid index, the way an
// interrupted concurrent build leaves it: a unique build over duplicates fails
// after its catalog entry is committed.
func leaveInvalidIndex(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO background_items (id, value) VALUES (1, 'same'), (2, 'same')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY `+testIndex.Name+` ON background_items(value)`)
	require.Error(t, err)
	exists, valid := indexState(t, pool)
	require.True(t, exists)
	require.False(t, valid)
}

func TestBackgroundIndexBuilderBuildsMissingIndex(t *testing.T) {
	pool := newBackgroundIndexPool(t)
	if pool == nil {
		return
	}
	builder := BackgroundIndexBuilder{Pool: pool, LockKey: testIndexLockKey}
	require.True(t, builder.TryBuild(context.Background(), testIndex))
	exists, valid := indexState(t, pool)
	assert.True(t, exists)
	assert.True(t, valid)
	assert.True(t, builder.TryBuild(context.Background(), testIndex), "a valid index is left alone")
}

// While another replica holds the lock, a builder neither builds nor drops an
// invalid index: it may be that replica's build in progress.
func TestBackgroundIndexBuilderWaitsForLockHolder(t *testing.T) {
	pool := newBackgroundIndexPool(t)
	if pool == nil {
		return
	}
	leaveInvalidIndex(t, pool)
	release := holdLock(t, pool)

	builder := BackgroundIndexBuilder{Pool: pool, LockKey: testIndexLockKey}
	assert.False(t, builder.TryBuild(context.Background(), testIndex))
	exists, valid := indexState(t, pool)
	assert.True(t, exists, "the invalid index is not dropped while another session holds the lock")
	assert.False(t, valid)

	release()
	_, err := pool.Exec(context.Background(), `DELETE FROM background_items WHERE id = 2`)
	require.NoError(t, err)
	assert.True(t, builder.TryBuild(context.Background(), testIndex), "the lock holder rebuilds an abandoned index")
	exists, valid = indexState(t, pool)
	assert.True(t, exists)
	assert.True(t, valid)
}

func TestBackgroundIndexBuilderRetriesUntilBuilt(t *testing.T) {
	pool := newBackgroundIndexPool(t)
	if pool == nil {
		return
	}
	release := holdLock(t, pool)
	time.AfterFunc(200*time.Millisecond, release)

	builder := BackgroundIndexBuilder{Pool: pool, LockKey: testIndexLockKey, Retry: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.True(t, builder.Build(ctx, testIndex))
	_, valid := indexState(t, pool)
	assert.True(t, valid)
}

func TestBackgroundIndexBuilderStopEndsRetries(t *testing.T) {
	pool := newBackgroundIndexPool(t)
	if pool == nil {
		return
	}
	holdLock(t, pool)

	stop := BackgroundIndexBuilder{Pool: pool, LockKey: testIndexLockKey, Retry: time.Hour}.Start(testIndex)
	done := make(chan struct{})
	go func() {
		stop()
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not end the retry wait")
	}
	exists, _ := indexState(t, pool)
	assert.False(t, exists)
}

// An index that keeps failing does not hold up the ones after it.
func TestBackgroundIndexBuilderBuildsPastAFailingIndex(t *testing.T) {
	pool := newBackgroundIndexPool(t)
	if pool == nil {
		return
	}
	broken := BackgroundIndex{
		Name:   "idx_background_items_missing",
		Create: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_background_items_missing ON background_items(missing_column)`,
		Label:  "broken test",
	}
	builder := BackgroundIndexBuilder{Pool: pool, LockKey: testIndexLockKey, Retry: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	built := make(chan bool, 1)
	go func() { built <- builder.Build(ctx, broken, testIndex) }()

	// Waits for the index itself, not a deadline, so a slow server only
	// makes the test slower.
	require.Eventually(t, func() bool {
		_, valid, err := backgroundIndexState(context.Background(), pool, testIndex.Name)
		return err == nil && valid
	}, 30*time.Second, 20*time.Millisecond, "the index after the broken one is built anyway")
	cancel()
	assert.False(t, <-built, "the broken index never becomes valid")
}
