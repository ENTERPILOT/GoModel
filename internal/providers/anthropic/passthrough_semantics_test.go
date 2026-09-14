package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricher_Enrich(t *testing.T) {
	enricher := passthroughSemanticEnricher

	tests := []struct {
		name          string
		info          *core.PassthroughRouteInfo
		wantOperation string
		wantAuditPath string
	}{
		{
			name:          "messages",
			info:          &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "messages", NormalizedEndpoint: "messages"},
			wantOperation: "anthropic.messages",
			wantAuditPath: "/v1/messages",
		},
		{
			name:          "messages batches",
			info:          &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "v1/messages/batches", NormalizedEndpoint: "messages/batches"},
			wantOperation: "anthropic.messages_batches",
			wantAuditPath: "/v1/messages/batches",
		},
		{
			name:          "default uses normalized endpoint",
			info:          &core.PassthroughRouteInfo{Provider: "anthropic", RawEndpoint: "v1/other", NormalizedEndpoint: "other"},
			wantOperation: "",
			wantAuditPath: "/p/anthropic/other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enricher.Enrich(nil, nil, tt.info)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantOperation, got.SemanticOperation)
			assert.Equal(t, tt.wantAuditPath, got.AuditPath)
		})
	}
}
