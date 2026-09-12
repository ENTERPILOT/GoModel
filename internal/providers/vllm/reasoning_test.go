package vllm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentMessages returns the messages array of the last recorded request.
func sentMessages(t *testing.T, capture *providertest.Capture) []any {
	t.Helper()
	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok, "request body has no messages array")
	return messages
}

func TestChatCompletion_RenamesLegacyReasoningContentOnAssistantMessages(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ChatRequest
	err := json.Unmarshal([]byte(`{
		"model":"Qwen3.8-27B",
		"messages":[
			{"role":"user","content":"what is the median life-expectancy of a cat"},
			{"role":"assistant","content":"12-15 years","reasoning_content":"the user wants a quick factual answer"},
			{"role":"user","content":"and a dog's?"}
		]
	}`), &req)
	require.NoError(t, err)

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	messages := sentMessages(t, capture)
	require.Len(t, messages, 3)
	assistantMsg, _ := messages[1].(map[string]any)
	assert.Equal(t, "the user wants a quick factual answer", assistantMsg["reasoning"])
	assert.NotContains(t, assistantMsg, "reasoning_content")
	assert.Nil(t, req.Messages[1].ExtraFields.Lookup("reasoning"), "caller's request must not be mutated")
}

func TestChatCompletion_DoesNotOverrideExistingReasoningField(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ChatRequest
	err := json.Unmarshal([]byte(`{
		"model":"Qwen3.8-27B",
		"messages":[
			{"role":"assistant","content":"12-15 years","reasoning":"current field","reasoning_content":"stale legacy value"}
		]
	}`), &req)
	require.NoError(t, err)

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	messages := sentMessages(t, capture)
	require.Len(t, messages, 1)
	assistantMsg, _ := messages[0].(map[string]any)
	assert.Equal(t, "current field", assistantMsg["reasoning"])
}

func TestAdaptChatRequest_NoOpWithoutLegacyReasoningContent(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{Role: "assistant", Content: "hi"}},
	}

	adapted, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.Same(t, req, adapted)
}

func TestAdaptChatRequest_IgnoresNonAssistantMessages(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{Role: "tool", Content: "ok"}},
	}
	req.Messages[0].ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"reasoning_content": json.RawMessage(`"should not move"`),
	})

	adapted, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.Same(t, req, adapted)
}

func TestAdaptChatRequest_NilRequest(t *testing.T) {
	adapted, err := adaptChatRequest(nil)
	require.NoError(t, err)
	assert.Nil(t, adapted)
}

// TestChatCompletion_AppliesAdaptChatRequestThroughStandardConstructor covers
// the production wiring path: New (used by the factory from config), not
// NewWithHTTPClient (test-only). The other tests in this file construct the
// provider via NewWithHTTPClient, which would not catch AdaptChatRequest
// being wired into one constructor but not the other.
func TestChatCompletion_AppliesAdaptChatRequestThroughStandardConstructor(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ChatRequest
	err := json.Unmarshal([]byte(`{
		"model":"Qwen3.8-27B",
		"messages":[
			{"role":"assistant","content":"12-15 years","reasoning_content":"prior turn reasoning"}
		]
	}`), &req)
	require.NoError(t, err)

	provider, ok := New(providers.ProviderConfig{BaseURL: server.URL}, providers.ProviderOptions{}).(*Provider)
	require.True(t, ok)
	_, err = provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	messages := sentMessages(t, capture)
	require.Len(t, messages, 1)
	assistantMsg, _ := messages[0].(map[string]any)
	assert.Equal(t, "prior turn reasoning", assistantMsg["reasoning"])
}

// TestAdaptChatRequest_SkipsMalformedReasoningContentWithoutError documents
// that a syntactically invalid reasoning_content value is never seen by
// adaptChatRequest at all: UnknownJSONFields.Lookup decodes each value with
// a streaming json.Decoder, so a malformed value fails to decode and Lookup
// returns nil (see UnknownJSONFields.Lookup) rather than surfacing invalid
// bytes. adaptChatRequest then treats the field as absent. There is no
// reachable path back into core.MergeUnknownJSONFields with invalid JSON
// here, since Lookup only ever returns bytes it has already decoded
// successfully, so this request is left unmodified rather than erroring.
func TestAdaptChatRequest_SkipsMalformedReasoningContentWithoutError(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{Role: "assistant", Content: "hi"}},
	}
	req.Messages[0].ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"reasoning_content": json.RawMessage(`not-valid-json{{{`),
	})

	adapted, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.Same(t, req, adapted)
}
