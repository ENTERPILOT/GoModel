package opencodego

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// responsesOnlyBody is a minimal completed Responses payload.
const responsesOnlyBody = `{
	"id":"resp_1",
	"object":"response",
	"status":"completed",
	"model":"muse-spark-1.3-contributor",
	"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed",
		"content":[{"type":"output_text","text":"hello","annotations":[]}]}],
	"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}
}`

// responsesStreamBody is a minimal Responses SSE stream.
const responsesStreamBody = `event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"muse-spark-1.3-contributor","output":[],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}

`

// responsesServer answers both the Responses and the chat endpoints so a test
// can assert which one the provider chose.
func responsesServer(t *testing.T) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(responsesStreamBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/chat/completions" {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","created":1,"model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
			return
		}
		_, _ = w.Write([]byte(responsesOnlyBody))
	})
}

func TestChatCompletion_ResponsesOnlyModel_UsesResponses(t *testing.T) {
	server, capture := responsesServer(t)
	ctx := core.WithSessionID(context.Background(), "session-resp")

	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest("muse-spark-1.3-contributor"))
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/responses", req.Path)
	assert.Equal(t, "session-resp", req.Header.Get("x-opencode-session"))
	assert.Equal(t, "muse-spark-1.3-contributor", req.JSON(t)["model"])
}

// TestChatCompletion_ResponsesOnlyModel_AppliesReasoningHook pins the bypass
// the Responses route introduces: CompatibleProvider.ChatCompletion applies
// AdaptChatRequest, but ChatViaResponses does not, so the provider must. OpenCode
// Zen rejects requests that leave reasoning out and only accepts low/high/max.
func TestChatCompletion_ResponsesOnlyModel_AppliesReasoningHook(t *testing.T) {
	server, capture := responsesServer(t)

	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "muse-spark-1.3-contributor",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "xhigh"},
	})
	require.NoError(t, err)

	body := capture.Last(t).JSON(t)
	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok, "missing reasoning object in %v", body)
	assert.Equal(t, "max", reasoning["effort"])
}

func TestChatCompletion_ChatModel_StaysOnChatCompletions(t *testing.T) {
	server, capture := responsesServer(t)

	_, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), chatRequest("glm-5.1"))
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

func TestStreamChatCompletion_ResponsesOnlyModel_UsesResponses(t *testing.T) {
	server, capture := responsesServer(t)

	stream, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "grok-4.7",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	req := capture.Last(t)
	assert.Equal(t, "/responses", req.Path)
	assert.Equal(t, true, req.JSON(t)["stream"])
}

// TestResponses_ResponsesOnlyModel_UsesNativeEndpoint pins the mirror rule: a
// client that already speaks Responses must not be translated down to chat and
// back.
func TestResponses_ResponsesOnlyModel_UsesNativeEndpoint(t *testing.T) {
	server, capture := responsesServer(t)

	resp, err := newTestProvider(server.URL, server.Client()).Responses(context.Background(), &core.ResponsesRequest{
		Model: "muse-spark-1.3-contributor",
		Input: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, "/responses", capture.Last(t).Path)
	require.Len(t, resp.Output, 1)
}

// TestResponses_ChatModel_TranslatesToChat covers the pre-existing behaviour
// the new route must not regress.
func TestResponses_ChatModel_TranslatesToChat(t *testing.T) {
	server, capture := responsesServer(t)

	_, err := newTestProvider(server.URL, server.Client()).Responses(context.Background(), &core.ResponsesRequest{
		Model: "glm-5.1",
		Input: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

func TestUsesResponses_OverrideToleratesPrefixAndWhitespace(t *testing.T) {
	t.Setenv(responsesModelsEnvVar, " custom-model , other-model ")
	p := newTestProvider("", nil)

	assert.True(t, p.usesResponses("opencode_go/custom-model"))
	assert.True(t, p.usesResponses("other-model"))
	assert.False(t, p.usesResponses("muse-spark-1.3-contributor"))
}
