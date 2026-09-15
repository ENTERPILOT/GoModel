package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestChatCompletion_MapsReasoningToZaiReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "glm-5.2",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "medium", sent["reasoning_effort"])
}

// The gateway builds Z.ai through Registration.New, not NewWithHTTPClient, so
// the adaptation must hold on that path too.
func TestNew_MapsReasoningToZaiReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := New(providers.ProviderConfig{APIKey: "zai-key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{}))
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "glm-5.3",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "high", sent["reasoning_effort"])
}

func TestChatCompletion_NormalizesReasoningEffortForGLM53(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "glm-5.3-flash",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "xhigh"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "max", sent["reasoning_effort"])
}

func TestChatCompletion_DropsReasoningWithoutEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "glm-5.3",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "  "},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.NotContains(t, sent, "reasoning_effort")
}

func TestChatCompletion_KeepsFlatReasoningEffortWithoutNestedReasoning(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "glm-5.3-flash",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"high"`),
		}),
	})
	require.NoError(t, err)

	assert.Equal(t, "high", capture.Last(t).JSON(t)["reasoning_effort"])
}

func TestResponses_MapsReasoningToZaiReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:     "glm-5.3-flash",
		Input:     "hi",
		Reasoning: &core.Reasoning{Effort: "minimal"},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/chat/completions", req.Path)
	sent := req.JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "low", sent["reasoning_effort"])
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   string
	}{
		{"glm-5.3", "none", "low"},
		{"glm-5.3", "minimal", "low"},
		{"glm-5.3", "low", "low"},
		{"glm-5.3", "medium", "high"},
		{"glm-5.3", "high", "high"},
		{"glm-5.3", "xhigh", "max"},
		{"glm-5.3", "max", "max"},
		{"glm-5.3-flash", " MEDIUM ", "high"},
		{"zai/glm-5.3-flash", "none", "low"},
		{"glm-5.3", "custom", "custom"},
		{"glm-5.2", "none", "none"},
		{"glm-5.2", "medium", "medium"},
		{"glm-4.7-flash", "xhigh", "xhigh"},
		{"glm-5.30", "medium", "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.model+"/"+tt.effort, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeReasoningEffort(tt.model, tt.effort))
		})
	}
}
