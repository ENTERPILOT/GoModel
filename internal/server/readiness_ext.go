package server

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/enterpilot/gomodel/ext"
)

// namedHealthChecker pairs an extension health checker with the component name
// it was accepted under. inFlight allows at most one outstanding call per
// checker, so a check that ignores cancellation cannot pile up goroutines
// across readiness probes.
type namedHealthChecker struct {
	name     string
	checker  ext.HealthChecker
	inFlight atomic.Bool
}

// validHealthCheckers drops checkers readiness cannot report unambiguously:
// nil ones, empty names, and names taken by a core component or an earlier
// checker.
func validHealthCheckers(checkers []ext.HealthChecker) []*namedHealthChecker {
	var valid []*namedHealthChecker
	for _, checker := range checkers {
		if isNilExtension(checker) {
			slog.Warn("readiness: ignoring nil extension health checker")
			continue
		}
		name := strings.TrimSpace(checker.Name())
		taken := slices.Contains(coreReadyComponents, name) ||
			slices.ContainsFunc(valid, func(hc *namedHealthChecker) bool { return hc.name == name })
		if name == "" || taken {
			slog.Warn("readiness: ignoring extension health checker with an empty or duplicate name", "name", name)
			continue
		}
		valid = append(valid, &namedHealthChecker{name: name, checker: checker})
	}
	return valid
}

// startExtensionHealthChecks runs every checker concurrently under one shared
// readinessProbeTimeout deadline and returns a function that waits for their
// results until that deadline. A checker that has not answered by then, is
// still busy with an earlier probe, or panics has no status in the result,
// which readiness reports as degraded.
func startExtensionHealthChecks(ctx context.Context, checkers []*namedHealthChecker) func() map[string]ext.HealthStatus {
	if len(checkers) == 0 {
		return func() map[string]ext.HealthStatus { return nil }
	}
	ctx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	results := newExtensionHealthResults(ctx)
	for _, hc := range checkers {
		if !hc.inFlight.CompareAndSwap(false, true) {
			slog.Warn("readiness: extension health check still running from an earlier probe", "component", hc.name)
			continue
		}
		results.expect()
		go func() {
			defer hc.inFlight.Store(false)
			var status ext.HealthStatus
			defer func() {
				if r := recover(); r != nil {
					slog.Error("readiness: extension health check panicked", "component", hc.name, "panic", r)
				}
				results.record(hc.name, status)
			}()
			status = hc.checker.CheckHealth(ctx)
		}()
	}

	return func() map[string]ext.HealthStatus {
		defer cancel()
		return results.collect()
	}
}

// extensionHealthResults gathers checker answers for one readiness probe. An
// answer counts only when it is recorded before the deadline, and checking the
// deadline and storing the answer happen under the same lock that collect
// takes, so an in-time answer is never lost and a late one is never accepted.
type extensionHealthResults struct {
	ctx      context.Context
	mu       sync.Mutex
	statuses map[string]ext.HealthStatus
	pending  int
	closed   bool
	done     chan struct{}
}

func newExtensionHealthResults(ctx context.Context) *extensionHealthResults {
	return &extensionHealthResults{ctx: ctx, statuses: map[string]ext.HealthStatus{}, done: make(chan struct{})}
}

// expect registers one more started check; call it before the check starts.
func (r *extensionHealthResults) expect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending++
}

// record stores an answer unless the deadline has passed or collection ended.
func (r *extensionHealthResults) record(name string, status ext.HealthStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return
	}
	r.statuses[name] = status
	r.pending--
	if r.pending == 0 {
		close(r.done)
	}
}

// collect waits until every started check has answered or the deadline
// passes, then returns the answers recorded in time. Later answers are ignored.
func (r *extensionHealthResults) collect() map[string]ext.HealthStatus {
	r.mu.Lock()
	waiting := r.pending > 0
	r.mu.Unlock()
	if waiting {
		select {
		case <-r.done:
		case <-r.ctx.Done():
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.pending > 0 {
		slog.Warn("readiness: extension health checks did not finish before the deadline", "missing", r.pending)
	}
	return r.statuses
}
