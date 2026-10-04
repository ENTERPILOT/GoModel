package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
)

type funcHealthChecker func(context.Context) ext.HealthStatus

func (funcHealthChecker) Name() string { return "vaults" }

func (f funcHealthChecker) CheckHealth(ctx context.Context) ext.HealthStatus { return f(ctx) }

// awaitExtensionHealthCheck waits until the checker's goroutine has buffered
// its result and released its in-flight slot.
func awaitExtensionHealthCheck(t *testing.T, hc *namedHealthChecker) {
	t.Helper()
	require.Eventually(t, func() bool { return !hc.inFlight.Load() }, time.Second, time.Millisecond)
}

func TestExtensionHealthDiscardsAnswerAfterDeadline(t *testing.T) {
	hc := &namedHealthChecker{name: "vaults", checker: funcHealthChecker(func(ctx context.Context) ext.HealthStatus {
		<-ctx.Done()
		return ext.HealthOK
	})}
	ctx, cancel := context.WithCancel(context.Background())
	wait := startExtensionHealthChecks(ctx, []*namedHealthChecker{hc})

	cancel()
	awaitExtensionHealthCheck(t, hc)

	// The late result and the expired deadline are both ready here; the
	// answer must still be rejected every time.
	assert.Empty(t, wait()["vaults"])
}

func TestExtensionHealthKeepsInTimeAnswerCollectedAfterDeadline(t *testing.T) {
	hc := &namedHealthChecker{name: "vaults", checker: funcHealthChecker(func(context.Context) ext.HealthStatus {
		return ext.HealthDown
	})}
	ctx, cancel := context.WithCancel(context.Background())
	wait := startExtensionHealthChecks(ctx, []*namedHealthChecker{hc})

	awaitExtensionHealthCheck(t, hc)
	cancel()

	assert.Equal(t, ext.HealthDown, wait()["vaults"])
}

func TestExtensionHealthResultsAcceptOnlyAnswersRecordedBeforeTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	results := newExtensionHealthResults(ctx, 3)

	results.record("in-time", ext.HealthDown)
	cancel()
	results.record("late", ext.HealthOK)

	got := results.collect()
	results.record("after-collect", ext.HealthOK)

	assert.Equal(t, map[string]ext.HealthStatus{"in-time": ext.HealthDown}, got)
}

func TestExtensionHealthManyFastChecksAllReport(t *testing.T) {
	checkers := make([]*namedHealthChecker, 64)
	for i := range checkers {
		checkers[i] = &namedHealthChecker{
			name:    fmt.Sprintf("ext-%d", i),
			checker: funcHealthChecker(func(context.Context) ext.HealthStatus { return ext.HealthOK }),
		}
	}

	for range 20 {
		statuses := startExtensionHealthChecks(context.Background(), checkers)()
		require.Len(t, statuses, len(checkers))
		for _, hc := range checkers {
			awaitExtensionHealthCheck(t, hc)
		}
	}
}
