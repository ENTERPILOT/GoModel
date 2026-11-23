package fireworks

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
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
			provider := newTestProvider("test-api-key", server.URL, nil, llmclient.Hooks{})

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

// Fireworks answers 400 "Extra inputs are not permitted" to annotations, audio
// and function_call on a replayed assistant message, with or without a value;
// refusal, name and reasoning_content are part of its schema and stay.
func TestChatCompletion_DropsRejectedAssistantMembers(t *testing.T) {
	const body = `{"model":"accounts/fireworks/models/gpt-oss-120b","messages":[` +
		`{"role":"user","content":"17*23?","name":"alice"},` +
		`{"role":"assistant","content":"391","name":"bot","refusal":null,"annotations":[],"audio":{"id":"audio_1"},` +
		`"function_call":{"name":"f","arguments":"{}"},"reasoning_content":"17*23"},` +
		`{"role":"user","content":"sure?"}]}`
	want := map[string]any{"role": "assistant", "content": "391", "name": "bot", "refusal": nil, "reasoning_content": "17*23"}

	t.Run("buffered", func(t *testing.T) {
		server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
		provider := newTestProvider("test-api-key", server.URL, nil, llmclient.Hooks{})
		var req core.ChatRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))

		_, err := provider.ChatCompletion(context.Background(), &req)
		require.NoError(t, err)

		messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
		require.True(t, ok)
		require.Len(t, messages, 3)
		assert.Equal(t, want, messages[1])
		assert.Equal(t, map[string]any{"role": "user", "content": "17*23?", "name": "alice"}, messages[0])
		assert.True(t, req.Messages[1].ExtraFields.HasAny("annotations"), "the caller's request must not change")
	})

	t.Run("streaming", func(t *testing.T) {
		server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
		provider := newTestProvider("test-api-key", server.URL, nil, llmclient.Hooks{})
		var req core.ChatRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))

		stream, err := provider.StreamChatCompletion(context.Background(), &req)
		require.NoError(t, err)
		_, err = io.ReadAll(stream)
		require.NoError(t, err)
		require.NoError(t, stream.Close())

		messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
		require.True(t, ok)
		require.Len(t, messages, 3)
		assert.Equal(t, want, messages[1])
	})
}
