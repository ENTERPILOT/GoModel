package openai

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
			name:          "responses",
			info:          &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "responses", NormalizedEndpoint: "responses"},
			wantOperation: "openai.responses",
			wantAuditPath: "/v1/responses",
		},
		{
			name:          "chat completions",
			info:          &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "v1/chat/completions", NormalizedEndpoint: "chat/completions"},
			wantOperation: "openai.chat_completions",
			wantAuditPath: "/v1/chat/completions",
		},
		{
			name:          "embeddings",
			info:          &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "embeddings", NormalizedEndpoint: "embeddings"},
			wantOperation: "openai.embeddings",
			wantAuditPath: "/v1/embeddings",
		},
		{
			name:          "default uses normalized endpoint",
			info:          &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "v1/fine_tuning/jobs", NormalizedEndpoint: "fine_tuning/jobs"},
			wantOperation: "",
			wantAuditPath: "/p/openai/fine_tuning/jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enricher.Enrich(nil, nil, tt.info)
			if got == nil {
				t.Fatal("Enrich() = nil")
				return
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
