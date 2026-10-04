package server

import (
	"context"
	"log/slog"
	"slices"
	"strings"
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

type extensionHealthResult struct {
	name   string
	status ext.HealthStatus
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
// which readiness reports as degraded. Each answer is stamped against the
// deadline when the checker returns, so a late answer is always discarded and
// an in-time one is kept even when the caller collects after the deadline.
func startExtensionHealthChecks(ctx context.Context, checkers []*namedHealthChecker) func() map[string]ext.HealthStatus {
	if len(checkers) == 0 {
		return func() map[string]ext.HealthStatus { return nil }
	}
	ctx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	// Buffered so a checker that answers after the deadline never blocks.
	results := make(chan extensionHealthResult, len(checkers))
	started := 0
	for _, hc := range checkers {
		if !hc.inFlight.CompareAndSwap(false, true) {
			slog.Warn("readiness: extension health check still running from an earlier probe", "component", hc.name)
			continue
		}
		started++
		go func() {
			defer hc.inFlight.Store(false)
			result := extensionHealthResult{name: hc.name}
			defer func() {
				if r := recover(); r != nil {
					slog.Error("readiness: extension health check panicked", "component", hc.name, "panic", r)
				}
				results <- result
			}()
			status := hc.checker.CheckHealth(ctx)
			if ctx.Err() == nil {
				result.status = status
			}
		}()
	}

	return func() map[string]ext.HealthStatus {
		defer cancel()
		statuses := make(map[string]ext.HealthStatus, len(checkers))
		for range started {
			select {
			case r := <-results:
				statuses[r.name] = r.status
			case <-ctx.Done():
				// Keep answers that already arrived: select picks randomly
				// when both cases are ready.
				drainExtensionHealthResults(results, statuses)
				if len(statuses) < started {
					slog.Warn("readiness: extension health checks did not finish before the deadline", "error", ctx.Err())
				}
				return statuses
			}
		}
		return statuses
	}
}

// drainExtensionHealthResults records every result already buffered.
func drainExtensionHealthResults(results <-chan extensionHealthResult, statuses map[string]ext.HealthStatus) {
	for {
		select {
		case r := <-results:
			statuses[r.name] = r.status
		default:
			return
		}
	}
}
