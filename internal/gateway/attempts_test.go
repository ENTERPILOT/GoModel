package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// A failed attempt must notify the observer immediately (so the live audit
// preview can surface a failed primary while failover is still in flight),
// while a successful attempt must not — keeping the success path free of extra
// live publishes.
func TestRecordProviderAttemptNotifiesObserverOnFailureOnly(t *testing.T) {
	calls := 0
	ctx := WithAttemptObserver(WithAttemptRecorder(context.Background()), func() { calls++ })

	recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, "openai", "openai", "gpt-4o", time.Now(), nil))
	require.Equal(t, 0, calls)

	recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, "azure", "azure", "gpt-4o", time.Now(), core.NewNotFoundError("model not available")))
	require.Equal(t, 1, calls)
	got := AttemptsFromContext(ctx)
	require.Len(t, got, 2)
}
