package anthropic

import (
	"testing"

	"gomodel/internal/core"
)

func TestPassthroughSemanticEnricher_Enrich(t *testing.T) {
	enricher := passthroughSemanticEnricher{}

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
			if got == nil {
				t.Fatal("Enrich() = nil")
			}
			if got.SemanticOperation != tt.wantOperation {
				t.Fatalf("SemanticOperation = %q, want %q", got.SemanticOperation, tt.wantOperation)
			}
			if got.AuditPath != tt.wantAuditPath {
				t.Fatalf("AuditPath = %q, want %q", got.AuditPath, tt.wantAuditPath)
			}
		})
	}
}
