package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// The count comes from OpenAI's /responses/input_tokens for the request as the
// Responses translation renders it, without the members that do not affect
// the input (stop, output limit, storage).
func TestCountChatTokens(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/responses/input_tokens": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"response.input_tokens","input_tokens":130}`)
		},
	})
	provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
	maxTokens := 1
	got, err := provider.CountChatTokens(context.Background(), &core.ChatRequest{
		Model: "gpt-4o", MaxTokens: &maxTokens, Tools: weatherTool,
		Messages:    []core.Message{{Role: "system", Content: "Be terse."}, {Role: "user", Content: "hello"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"stop": json.RawMessage(`["END"]`)}),
	})
	require.NoError(t, err)
	assert.Equal(t, 130, got)

	sent := capture.Last(t)
	assert.Equal(t, "/responses/input_tokens", sent.Path)
	body := sent.JSON(t)
	assert.Equal(t, "gpt-4o", body["model"])
	assert.Len(t, body["input"], 2)
	assert.Len(t, body["tools"], 1)
	for _, member := range []string{"stop", "store", "max_output_tokens"} {
		assert.NotContains(t, body, member)
	}
}

// A request the translation cannot carry exactly is left to the estimate.
func TestCountChatTokens_UntranslatableIsUnsupported(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"input_tokens":1}`)
	provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
	_, err := provider.CountChatTokens(context.Background(), &core.ChatRequest{
		Model: "gpt-6-luna", Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"logprobs": json.RawMessage(`true`)}),
	})
	require.ErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
	assert.Zero(t, capture.Count())
}
