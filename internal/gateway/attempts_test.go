package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// A failed attempt must notify the observer immediately (so the live audit
// preview can surface a failed primary while failover is still in flight),
// while a successful attempt must not — keeping the success path free of extra
// live publishes.
func TestRecordProviderAttemptNotifiesObserverOnFailureOnly(t *testing.T) {
	calls := 0
	ctx := WithAttemptObserver(WithAttemptRecorder(context.Background()), func() { calls++ })

	recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, "openai", "openai", "gpt-4o", time.Now(), nil))
	if calls != 0 {
		t.Fatalf("observer fired on a successful attempt: calls=%d, want 0", calls)
	}

	recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, "azure", "azure", "gpt-4o", time.Now(), core.NewNotFoundError("model not available")))
	if calls != 1 {
		t.Fatalf("observer did not fire on a failed attempt: calls=%d, want 1", calls)
	}

	if got := AttemptsFromContext(ctx); len(got) != 2 {
		t.Fatalf("recorded attempts = %d, want 2", len(got))
	}
}
