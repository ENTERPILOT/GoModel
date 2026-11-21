package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestApplyWorkflowContextOverrides(t *testing.T) {
	failoverOff := core.WithRequestControls(context.Background(), core.RequestControls{FailoverOff: true})
	allOn := core.DefaultWorkflowFeatures()

	tests := []struct {
		name         string
		ctx          context.Context
		features     core.WorkflowFeatures
		wantFailover bool
		wantGuards   bool
	}{
		{name: "no controls", ctx: context.Background(), features: allOn, wantFailover: true, wantGuards: true},
		{name: "client turned failover off", ctx: failoverOff, features: allOn, wantFailover: false, wantGuards: true},
		{
			// The client's control covers its own request, not the inference
			// a guardrail issues while serving it.
			name:         "guardrail call keeps the configured failover",
			ctx:          core.WithRequestOrigin(failoverOff, core.RequestOriginGuardrail),
			features:     allOn,
			wantFailover: true,
			wantGuards:   false,
		},
		{
			name:         "plugin call keeps the configured failover",
			ctx:          core.WithRequestOrigin(failoverOff, core.RequestOriginPlugin),
			features:     allOn,
			wantFailover: true,
			wantGuards:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := &core.ResolvedWorkflowPolicy{VersionID: "wf-1", Features: tt.features, GuardrailsHash: "hash"}
			workflow := &core.Workflow{Policy: matched}

			ApplyWorkflowContextOverrides(tt.ctx, workflow)

			assert.Equal(t, tt.wantFailover, workflow.FailoverEnabled())
			assert.Equal(t, tt.wantGuards, workflow.GuardrailsEnabled())
			assert.Equal(t, allOn, matched.Features, "the matched policy is shared and must not be edited in place")
		})
	}
}
