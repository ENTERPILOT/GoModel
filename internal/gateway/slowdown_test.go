package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestWaitForInferenceSlowdownAddsFactorOfInferenceTime(t *testing.T) {
	workflow := &core.Workflow{Resolution: &core.RequestModelResolution{Slowdown: 0.5}}
	started := time.Now()
	if err := waitForInferenceSlowdown(context.Background(), workflow, 20*time.Millisecond); err != nil {
		t.Fatalf("waitForInferenceSlowdown() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < 8*time.Millisecond {
		t.Fatalf("waitForInferenceSlowdown() waited %v, want about 10ms", elapsed)
	}
}

func TestWaitForInferenceSlowdownStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	workflow := &core.Workflow{Resolution: &core.RequestModelResolution{Slowdown: 10}}
	if err := waitForInferenceSlowdown(ctx, workflow, time.Minute); err != context.Canceled {
		t.Fatalf("waitForInferenceSlowdown() error = %v, want context.Canceled", err)
	}
}
