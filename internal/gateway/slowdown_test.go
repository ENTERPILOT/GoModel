package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestWaitForInferenceSlowdown(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		workflow  *core.Workflow
		inference time.Duration
		wantMin   time.Duration
		wantMax   time.Duration
		wantErr   error
	}{
		{name: "nil workflow", ctx: context.Background(), inference: time.Second, wantMax: 250 * time.Millisecond},
		{name: "zero factor", ctx: context.Background(), workflow: slowdownWorkflow(0), inference: time.Second, wantMax: 250 * time.Millisecond},
		{name: "positive factor", ctx: context.Background(), workflow: slowdownWorkflow(0.5), inference: 20 * time.Millisecond, wantMin: 8 * time.Millisecond, wantMax: 250 * time.Millisecond},
		{name: "canceled context", ctx: canceled, workflow: slowdownWorkflow(10), inference: time.Minute, wantMax: 250 * time.Millisecond, wantErr: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := time.Now()
			err := waitForInferenceSlowdown(tt.ctx, tt.workflow, tt.inference)
			elapsed := time.Since(started)
			require.ErrorIs(t, err, tt.wantErr)
			require.GreaterOrEqual(t, elapsed, tt.wantMin)

			if tt.wantMax > 0 {
				require.LessOrEqual(t, elapsed, tt.wantMax, "waitForInferenceSlowdown() waited too long")
			}
		})
	}
}

func slowdownWorkflow(factor float64) *core.Workflow {
	return &core.Workflow{Resolution: &core.RequestModelResolution{Slowdown: factor}}
}
