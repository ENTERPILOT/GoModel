package cohere

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
		if info == nil || info.GenAIOperation != want {
			t.Fatalf("GenAIOperation for %q = %+v, want %q", endpoint, info, want)
		}
	}
}
