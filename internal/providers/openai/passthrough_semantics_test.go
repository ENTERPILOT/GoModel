package openai

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricher_Enrich(t *testing.T) {
	enricher := passthroughSemanticEnricher

	tests := []struct {
		name               string
		info               *core.PassthroughRouteInfo
		wantOperation      string
		wantGenAI          string
		wantRequestLogPath string
	}{
		{
			name:               "responses",
			info:               &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "responses", NormalizedEndpoint: "responses"},
			wantOperation:      "openai.responses",
			wantGenAI:          "chat",
			wantRequestLogPath: "/v1/responses",
		},
		{
			name:               "chat completions",
			info:               &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "v1/chat/completions", NormalizedEndpoint: "chat/completions"},
			wantOperation:      "openai.chat_completions",
			wantGenAI:          "chat",
			wantRequestLogPath: "/v1/chat/completions",
		},
		{
			name:               "embeddings",
			info:               &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "embeddings", NormalizedEndpoint: "embeddings"},
			wantOperation:      "openai.embeddings",
			wantGenAI:          "embeddings",
			wantRequestLogPath: "/v1/embeddings",
		},
		{
			name:               "default uses normalized endpoint",
			info:               &core.PassthroughRouteInfo{Provider: "openai", RawEndpoint: "v1/fine_tuning/jobs", NormalizedEndpoint: "fine_tuning/jobs"},
			wantOperation:      "",
			wantRequestLogPath: "/p/openai/fine_tuning/jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enricher.Enrich(nil, nil, tt.info)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantOperation, got.SemanticOperation)
			assert.Equal(t, tt.wantGenAI, got.GenAIOperation)
			assert.Equal(t, tt.wantRequestLogPath, got.RequestLogPath)
		})
	}
}
