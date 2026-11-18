package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// A failed attempt must notify the observer immediately (so the live request log
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

// An attempt records whether its error message came from the provider, so
// the request log can withhold it with the body.
func TestProviderAttemptFromResultMarksUpstreamMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "gateway message", err: core.NewProviderError("openai", http.StatusBadGateway, "upstream connection failed", nil), want: false},
		{name: "upstream message", err: core.NewProviderError("openai", http.StatusBadGateway, "boom", nil).WithUpstreamMessage(), want: true},
		{name: "parsed upstream body", err: core.ParseProviderError("openai", http.StatusBadRequest, []byte(`{"error":{"message":"bad"}}`), nil), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempt := providerAttemptFromResult(AttemptKindPrimary, "openai", "openai", "gpt-4o", time.Now(), tt.err)
			assert.Equal(t, tt.want, attempt.UpstreamErrorMessage)
		})
	}
}
