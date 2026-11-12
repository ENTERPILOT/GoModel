package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// testSweepLockKey is this test's own lock, so it never contends with the
// sweeps other packages' tests run against the same server.
const testSweepLockKey int64 = 0x746573742d737770

func TestLockedSweep(t *testing.T) {
	tests := []struct {
		name    string
		tryLock func(ctx context.Context, fn func()) (bool, error)
		wantRun bool
	}{
		{"lock taken", func(_ context.Context, fn func()) (bool, error) { fn(); return true, nil }, true},
		{"lock held elsewhere", func(context.Context, func()) (bool, error) { return false, nil }, false},
		{"lock error", func(context.Context, func()) (bool, error) { return false, errors.New("down") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			lockedSweep(tt.tryLock, func() { ran = true })()
			assert.Equal(t, tt.wantRun, ran)
		})
	}
}

func TestSingleReplicaSweepWithoutPoolIsUnchanged(t *testing.T) {
	ran := false
	SingleReplicaSweep(nil, testSweepLockKey, func() { ran = true })()
	assert.True(t, ran)
}

// TestSingleReplicaSweepPostgreSQL runs two replicas' sweeps against one
// database at the same moment: only one of them sweeps, and the lock is free
// again for the next round.
func TestSingleReplicaSweepPostgreSQL(t *testing.T) {
	pool := sqlxtest.NewPostgresPool(t)
	if pool == nil {
		return // skipped: no test server configured
	}

	var sweeps atomic.Int32
	inSweep := make(chan struct{})
	finish := make(chan struct{})
	first := SingleReplicaSweep(pool, testSweepLockKey, func() {
		sweeps.Add(1)
		close(inSweep)
		<-finish
	})
	second := SingleReplicaSweep(pool, testSweepLockKey, func() { sweeps.Add(1) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		first()
	}()
	<-inSweep
	second()
	assert.Equal(t, int32(1), sweeps.Load(), "the second replica skips while the first sweeps")
	close(finish)
	<-done

	second()
	assert.Equal(t, int32(2), sweeps.Load(), "the lock is released after the sweep")
}
