package openrouter

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPassthroughSemanticEnricherUsesOpenRouterType(t *testing.T) {
	enricher := Registration.PassthroughSemanticEnricher
	if enricher == nil {
		t.Fatal("registration passthrough enricher is nil")
	}
	if got := enricher.ProviderType(); got != "openrouter" {
		t.Fatalf("ProviderType() = %q, want openrouter", got)
	}
	info := enricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		Provider: "openrouter", NormalizedEndpoint: "chat/completions",
	})
	if info == nil || info.GenAIOperation != "chat" || info.SemanticOperation != "openrouter.chat_completions" {
		t.Fatalf("enriched info = %+v, want OpenRouter chat semantics", info)
	}
}
