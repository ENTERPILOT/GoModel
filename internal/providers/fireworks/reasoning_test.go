package fireworks

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletion_MapsReasoningToReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "effort", effort: "low", wantEffort: "low"},
		{name: "empty effort drops it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := NewWithHTTPClient("test-api-key", server.URL, nil, llmclient.Hooks{})

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "accounts/fireworks/models/glm-5p2",
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning")
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "reasoning_effort")
				return
			}
			assert.Equal(t, tt.wantEffort, sent["reasoning_effort"])
		})
	}
}
