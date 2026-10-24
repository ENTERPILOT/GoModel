package deepseek

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricher_ProviderType(t *testing.T) {
	e := passthroughSemanticEnricher
	got := e.ProviderType()
	require.Equal(t, "deepseek", got)
}

func TestPassthroughSemanticEnricher_NilInfo_ReturnsNil(t *testing.T) {
	e := passthroughSemanticEnricher
	got := e.Enrich(nil, nil, nil)
	require.Nil(t, got)
}

func TestPassthroughSemanticEnricher_Enrich(t *testing.T) {
	e := passthroughSemanticEnricher

	tests := []struct {
		name               string
		rawEndpoint        string
		normalizedEndpoint string
		wantSemanticOp     string
		wantAuditPath      string
	}{
		{
			name:           "chat completions",
			rawEndpoint:    "/chat/completions",
			wantSemanticOp: "deepseek.chat_completions",
			wantAuditPath:  "/v1/chat/completions",
		},
		{
			name:           "FIM completions",
			rawEndpoint:    "/beta/completions",
			wantSemanticOp: "deepseek.fim_completions",
			wantAuditPath:  "/beta/completions",
		},
		{
			name:          "unknown endpoint gets prefixed audit path",
			rawEndpoint:   "/v1/models",
			wantAuditPath: "/p/deepseek/v1/models",
		},
		{
			name:               "NormalizedEndpoint takes precedence over RawEndpoint",
			rawEndpoint:        "/ignored",
			normalizedEndpoint: "/chat/completions",
			wantSemanticOp:     "deepseek.chat_completions",
			wantAuditPath:      "/v1/chat/completions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &core.PassthroughRouteInfo{
				RawEndpoint:        tc.rawEndpoint,
				NormalizedEndpoint: tc.normalizedEndpoint,
			}
			got := e.Enrich(nil, nil, info)
			require.NotNil(t, got)
			if tc.wantSemanticOp != "" {
				assert.Equal(t, tc.wantSemanticOp, got.SemanticOperation)
			}
			assert.Equal(t, tc.wantAuditPath, got.AuditPath)
		})
	}
}
