package zai

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricherUsesZAIType(t *testing.T) {
	enricher := Registration.PassthroughSemanticEnricher
	if enricher == nil {
		t.Fatal("registration passthrough enricher is nil")
	}
	if got := enricher.ProviderType(); got != "zai" {
		t.Fatalf("ProviderType() = %q, want zai", got)
	}
	info := enricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		Provider: "zai", NormalizedEndpoint: "embeddings",
	})
	if info == nil || info.GenAIOperation != "embeddings" || info.SemanticOperation != "zai.embeddings" || info.AuditPath != "/v1/embeddings" {
		t.Fatalf("enriched info = %+v, want Z.ai embedding semantics", info)
	}
}
