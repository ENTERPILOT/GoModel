package kilo

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSemanticEnricher(t *testing.T) {
	assert.Equal(t, "kilo", passthroughSemanticEnricher.ProviderType())

	got := passthroughSemanticEnricher.Enrich(nil, nil, &core.PassthroughRouteInfo{
		RawEndpoint:        "v1/chat/completions",
		NormalizedEndpoint: "chat/completions",
	})
	require.NotNil(t, got)
	assert.Equal(t, "kilo.chat_completions", got.SemanticOperation)
	assert.Equal(t, "/v1/chat/completions", got.AuditPath)
}
