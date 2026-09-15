package zai

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricherUsesZAIType(t *testing.T) {
	enricher := Registration.PassthroughSemanticEnricher
	require.NotNil(t, enricher)
	got := enricher.ProviderType()
	require.Equal(t, "zai", got)

	info := enricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		Provider: "zai", NormalizedEndpoint: "embeddings",
	})
	require.NotNil(t, info)
	require.Equal(t, "embeddings", info.GenAIOperation)
	require.Equal(t, "zai.embeddings", info.SemanticOperation)
	require.Equal(t, "/v1/embeddings", info.AuditPath)
}
