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

// The count follows the path that will serve the request: a Chat Completions
// tool request on GPT-5+ adds the Chat tools preamble, a Responses-served one
// does not, and a Chat-served text file is counted as the text Chat receives.
func TestCountChatTokens_CountsTheServingPath(t *testing.T) {
	textFile := []core.ContentPart{{Type: "file", File: &core.FileContent{FileData: "data:text/plain;base64,aGVsbG8=", Filename: "a.txt"}}}
	tests := []struct {
		name      string
		req       core.ChatRequest
		want      int
		wantInput any // the counted input, when checked
	}{
		{name: "chat-served tools on gpt-6", want: 50 + chatToolPreambleTokens,
			req: core.ChatRequest{Model: "gpt-6-luna", Tools: weatherTool}},
		{name: "responses-served tools on astra", want: 50,
			req: core.ChatRequest{Model: "gpt-6-astra", Tools: weatherTool}},
		{name: "no tools", want: 50, req: core.ChatRequest{Model: "gpt-6-luna"}},
		{name: "chat-served text file counted as text", want: 50,
			req:       core.ChatRequest{Model: "gpt-6-luna", Messages: []core.Message{{Role: "user", Content: textFile}}},
			wantInput: []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "a.txt\n\nhello"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"response.input_tokens","input_tokens":50}`)
			provider := New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
			req := tt.req
			if req.Messages == nil {
				req.Messages = []core.Message{{Role: "user", Content: "Weather in Paris?"}}
			}
			got, err := provider.CountChatTokens(context.Background(), &req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			if tt.wantInput != nil {
				assert.Equal(t, tt.wantInput, capture.Last(t).JSON(t)["input"])
			}
		})
	}
}
