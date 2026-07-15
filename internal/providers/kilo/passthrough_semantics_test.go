package kilo

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricher(t *testing.T) {
	if got := passthroughSemanticEnricher.ProviderType(); got != "kilo" {
		t.Fatalf("ProviderType() = %q, want kilo", got)
	}

	got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		RawEndpoint:        "v1/chat/completions",
		NormalizedEndpoint: "chat/completions",
	})
	if got == nil {
		t.Fatal("Enrich() returned nil")
	}
	if got.SemanticOperation != "kilo.chat_completions" || got.AuditPath != "/v1/chat/completions" {
		t.Fatalf("enriched info = %+v", got)
	}
}
