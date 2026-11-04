package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/plugins"
)

// passthroughCapturingProvider is a capturingProvider that also accepts
// native passthrough, so the native Messages gate can pass.
type passthroughCapturingProvider struct {
	capturingProvider
}

func (p *passthroughCapturingProvider) Passthrough(context.Context, string, *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return &core.PassthroughResponse{}, nil
}

func TestCanForwardMessagesNatively_Slowdown(t *testing.T) {
	tests := []struct {
		name     string
		slowdown float64
		want     bool
	}{
		{name: "no slowdown forwards natively", slowdown: 0, want: true},
		{name: "slowdown takes the translated path", slowdown: 0.5, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &translatedInferenceService{
				provider:     &passthroughCapturingProvider{},
				orchestrator: gateway.NewInferenceOrchestrator(gateway.InferenceConfig{}),
				pluginChains: staticChainsResolver{chains: &plugins.Chains{}},
			}
			workflow := &core.Workflow{
				ProviderType: anthropicProviderType,
				Resolution:   &core.RequestModelResolution{Slowdown: tt.slowdown},
			}
			assert.Equal(t, tt.want, svc.canForwardMessagesNatively(context.Background(), workflow, false))
		})
	}
}
