package cohere

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricherRecognizesCohereV2Inference(t *testing.T) {
	tests := map[string]string{
		"v2/chat":  "chat",
		"v2/embed": "embeddings",
	}
	for endpoint, want := range tests {
		info := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
			Provider: "cohere", RawEndpoint: endpoint, NormalizedEndpoint: endpoint,
		})
		require.NotNil(t, info, "endpoint %q", endpoint)
		assert.Equal(t, want, info.GenAIOperation, "endpoint %q", endpoint)
	}
}
