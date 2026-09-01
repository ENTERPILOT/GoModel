package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestProbeAvailability(t *testing.T) {
	probeErr := errors.New("backend unreachable")

	tests := []struct {
		name        string
		provider    core.Provider
		wantErr     error
		wantRecord  bool
		wantMessage string
	}{
		{
			name:     "provider without a checker is available and records nothing",
			provider: &registryMockProvider{},
		},
		{
			name:       "successful probe records an empty error",
			provider:   &initTestProvider{},
			wantRecord: true,
		},
		{
			name:        "failed probe is returned and recorded",
			provider:    &initTestProvider{availabilityErr: probeErr},
			wantErr:     probeErr,
			wantRecord:  true,
			wantMessage: probeErr.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewModelRegistry()

			err := registry.probeAvailability(t.Context(), tt.provider, "probe-target", availabilityProbeTimeout)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("probeAvailability() error = %v, want %v", err, tt.wantErr)
			}

			state, recorded := registry.providerRuntime["probe-target"]
			if recorded != tt.wantRecord {
				t.Fatalf("availability recorded = %t, want %t", recorded, tt.wantRecord)
			}
			if recorded && state.lastAvailabilityError != tt.wantMessage {
				t.Fatalf("recorded error = %q, want %q", state.lastAvailabilityError, tt.wantMessage)
			}
		})
	}
}

// The deadline lives at the call site now, so a provider that imposes no
// timeout of its own must still be bounded by the budget the caller passes.
func TestProbeAvailability_CallerDeadlineBoundsUntimedProvider(t *testing.T) {
	started := make(chan struct{})
	provider := &initTestProvider{
		checkAvailability: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	registry := NewModelRegistry()
	done := make(chan error, 1)
	go func() {
		done <- registry.probeAvailability(context.Background(), provider, "untimed", 50*time.Millisecond)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("probeAvailability() error = %v, want %v", err, context.DeadlineExceeded)
		}
	case <-time.After(time.Second):
		t.Fatal("probeAvailability() did not honor the caller's deadline")
	}

	if got := registry.providerRuntime["untimed"].lastAvailabilityError; got != context.DeadlineExceeded.Error() {
		t.Fatalf("recorded error = %q, want %q", got, context.DeadlineExceeded.Error())
	}
}
