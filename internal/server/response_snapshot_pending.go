package server

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// pendingSnapshot is one in-flight snapshot write: the user path it is
// written under and the channel closed when it lands.
type pendingSnapshot struct {
	userPath string
	done     chan struct{}
}

// trackPendingSnapshot registers an in-flight snapshot write for a response
// id, so a request chained on that response can wait for it.
func (s *translatedInferenceService) trackPendingSnapshot(id, userPath string) chan struct{} {
	done := make(chan struct{})
	s.pendingSnapshotMu.Lock()
	if s.pendingSnapshots == nil {
		s.pendingSnapshots = make(map[string]pendingSnapshot)
	}
	s.pendingSnapshots[id] = pendingSnapshot{userPath: userPath, done: done}
	s.pendingSnapshotMu.Unlock()
	return done
}

// finishPendingSnapshot releases the waiters of one snapshot write.
func (s *translatedInferenceService) finishPendingSnapshot(id string, done chan struct{}) {
	s.pendingSnapshotMu.Lock()
	if s.pendingSnapshots[id].done == done {
		delete(s.pendingSnapshots, id)
	}
	s.pendingSnapshotMu.Unlock()
	close(done)
}

// awaitPendingSnapshot blocks until the snapshot write for id, if one is in
// flight, has finished; it gives up with the request or after the write's
// own timeout, in which case the store lookup decides. Only a caller whose
// access scope covers the write's user path waits, so a tenant learns
// nothing about another tenant's in-flight response ids, while global and
// parent scopes keep their race protection.
func (s *translatedInferenceService) awaitPendingSnapshot(ctx context.Context, id string) {
	s.pendingSnapshotMu.Lock()
	pending, ok := s.pendingSnapshots[id]
	s.pendingSnapshotMu.Unlock()
	if !ok || !core.AccessScopeFromContext(ctx).Allows(pending.userPath) {
		return
	}
	timer := time.NewTimer(snapshotWriteTimeout)
	defer timer.Stop()
	select {
	case <-pending.done:
	case <-ctx.Done():
	case <-timer.C:
	}
}
