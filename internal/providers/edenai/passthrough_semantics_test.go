package edenai

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricher(t *testing.T) {
	require.Equal(t, "edenai", passthroughSemanticEnricher.ProviderType())

	tests := []struct {
		name               string
		rawEndpoint        string
		normalizedEndpoint string
		wantOperation      string
		wantGenAIOperation string
		wantAuditPath      string
	}{
		{
			name:               "chat completions",
			rawEndpoint:        "v1/chat/completions",
			normalizedEndpoint: "chat/completions",
			wantOperation:      "edenai.chat_completions",
			wantGenAIOperation: "chat",
			wantAuditPath:      "/v1/chat/completions",
		},
		{
			name:               "embeddings",
			rawEndpoint:        "v1/embeddings",
			normalizedEndpoint: "embeddings",
			wantOperation:      "edenai.embeddings",
			wantGenAIOperation: "embeddings",
			wantAuditPath:      "/v1/embeddings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
				RawEndpoint:        tt.rawEndpoint,
				NormalizedEndpoint: tt.normalizedEndpoint,
			})
			require.NotNil(t, got, "Enrich() returned nil")
			assert.Equal(t, tt.wantOperation, got.SemanticOperation)
			assert.Equal(t, tt.wantGenAIOperation, got.GenAIOperation)
			assert.Equal(t, tt.wantAuditPath, got.AuditPath)
		})
	}
}

// TestPassthroughSemanticEnricher_ResponsesIsNotOpenAIShaped asserts Eden's
// native /responses route is deliberately absent from the table. Eden's
// /v3/responses is not the OpenAI Responses API, so labelling it
// "edenai.responses" with a /v1/responses audit path would record a request as
// something it is not. It must fall through to the generic /p/edenai/... path.
func TestPassthroughSemanticEnricher_ResponsesIsNotOpenAIShaped(t *testing.T) {
	got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		RawEndpoint:        "v1/responses",
		NormalizedEndpoint: "responses",
	})
	require.NotNil(t, got, "Enrich() returned nil")
	assert.Empty(t, got.SemanticOperation, "SemanticOperation = %q, want empty: Eden /responses must not be advertised as OpenAI Responses", got.SemanticOperation)
	assert.Equal(t, "/p/edenai/responses", got.AuditPath)
}
