package openrouter

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricherUsesOpenRouterType(t *testing.T) {
	enricher := Registration.PassthroughSemanticEnricher
	require.NotNil(t, enricher)
	got := enricher.ProviderType()
	require.Equal(t, "openrouter", got)

	info := enricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		Provider: "openrouter", NormalizedEndpoint: "chat/completions",
	})
	require.NotNil(t, info)
	require.Equal(t, "chat", info.GenAIOperation)
	require.Equal(t, "openrouter.chat_completions", info.SemanticOperation)
	require.Equal(t, "/v1/chat/completions", info.AuditPath)
}
