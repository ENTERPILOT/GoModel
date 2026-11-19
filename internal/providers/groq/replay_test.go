package groq

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// sentAssistant returns the assistant message at index of the request body the
// upstream received.
func sentAssistant(t *testing.T, capture *providertest.Capture, index int) map[string]any {
	t.Helper()
	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Greater(t, len(messages), index)
	message, ok := messages[index].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "assistant", message["role"])
	return message
}

func chatRequest(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	var req core.ChatRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

// Groq answers 400 "property '...' is unsupported" to the members OpenAI
// returns on an assistant message and to the gateway's own reasoning_content,
// so a replayed turn is adapted instead of failing the next request.
func TestChatCompletion_AdaptsReplayedAssistantMessage(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		assistant     string
		wantReasoning any // nil means the member must be absent
		wantKept      map[string]any
	}{
		{
			name:          "an OpenAI SDK dump of a reasoning turn",
			model:         "openai/gpt-oss-20b",
			assistant:     `{"role":"assistant","content":"391","name":"bot","refusal":null,"annotations":[],"audio":null,"function_call":null,"reasoning_content":"17*23"}`,
			wantReasoning: "17*23",
			wantKept:      map[string]any{"content": "391", "name": "bot"},
		},
		{
			name:          "qwen3 takes replayed reasoning too",
			model:         "qwen/qwen3.8-27b",
			assistant:     `{"role":"assistant","content":"391","reasoning_content":"17*23"}`,
			wantReasoning: "17*23",
		},
		{
			name:      "a model without reasoning rejects the member, so the trace is dropped",
			model:     "llama-3.3-70b-versatile",
			assistant: `{"role":"assistant","content":"391","refusal":null,"reasoning_content":"17*23"}`,
		},
		{
			name:          "reasoning the client already sent wins",
			model:         "openai/gpt-oss-20b",
			assistant:     `{"role":"assistant","content":"391","reasoning":"client","reasoning_content":"gateway"}`,
			wantReasoning: "client",
		},
		{
			name:      "a legacy function call keeps its function_call",
			model:     "openai/gpt-oss-20b",
			assistant: `{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{}"}}`,
			wantKept:  map[string]any{"function_call": map[string]any{"name": "f", "arguments": "{}"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, minimalChatCompletionJSON)
			provider := newTestProvider(server.URL)
			req := chatRequest(t, `{"model":"`+tt.model+`","messages":[{"role":"user","content":"17*23?","name":"alice"},`+tt.assistant+`,{"role":"user","content":"sure?"}]}`)
			before := req.Messages[1].ExtraFields

			_, err := provider.ChatCompletion(context.Background(), req)
			require.NoError(t, err)

			assistant := sentAssistant(t, capture, 1)
			for _, key := range []string{"refusal", "annotations", "audio", "reasoning_content"} {
				assert.NotContains(t, assistant, key)
			}
			if tt.wantReasoning == nil {
				assert.NotContains(t, assistant, "reasoning")
			} else {
				assert.Equal(t, tt.wantReasoning, assistant["reasoning"])
			}
			if _, kept := tt.wantKept["function_call"]; !kept {
				assert.NotContains(t, assistant, "function_call")
			}
			for key, want := range tt.wantKept {
				assert.Equal(t, want, assistant[key], key)
			}
			user, ok := capture.Last(t).JSON(t)["messages"].([]any)[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "alice", user["name"], "Groq accepts name")
			assert.Equal(t, before, req.Messages[1].ExtraFields, "the caller's request must not change")
		})
	}
}

// The streaming path runs the same request adaptation.
func TestStreamChatCompletion_AdaptsReplayedAssistantMessage(t *testing.T) {
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := newTestProvider(server.URL)

	stream, err := provider.StreamChatCompletion(context.Background(), chatRequest(t,
		`{"model":"openai/gpt-oss-20b","stream":true,"messages":[{"role":"user","content":"17*23?"},`+
			`{"role":"assistant","content":"391","refusal":null,"annotations":[],"reasoning_content":"17*23"},`+
			`{"role":"user","content":"sure?"}]}`))
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assistant := sentAssistant(t, capture, 1)
	assert.Equal(t, map[string]any{"role": "assistant", "content": "391", "reasoning": "17*23"}, assistant)
}

// The Responses translation attaches a replayed reasoning item to the
// tool-call turn as reasoning_content, so an agent loop reaches Groq with the
// member it rejects unless the chat adaptation renames it.
func TestResponses_ReplaysReasoningItemAsGroqReasoning(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	var req core.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"openai/gpt-oss-20b","input":[`+
		`{"role":"user","content":"What time is it?"},`+
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"call the tool"}]},`+
		`{"type":"function_call","call_id":"call_1","name":"get_time","arguments":"{}"},`+
		`{"type":"function_call_output","call_id":"call_1","output":"12:00"}]}`), &req))

	_, err := provider.Responses(context.Background(), &req)
	require.NoError(t, err)

	assistant := sentAssistant(t, capture, 1)
	assert.Equal(t, "call the tool", assistant["reasoning"])
	assert.NotContains(t, assistant, "reasoning_content")
	assert.Contains(t, assistant, "tool_calls")
}

// A request that carries none of the adapted members reaches the hook's
// caller unchanged, without a copy.
func TestAdaptReplayedMessages_ReturnsRequestWhenNothingChanges(t *testing.T) {
	req := chatRequest(t, `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hi","name":"alice"},`+
		`{"role":"assistant","content":"hello","reasoning":"kept","name":"bot"}]}`)

	got, err := adaptReplayedMessages(req)

	require.NoError(t, err)
	assert.Same(t, req, got)
}
