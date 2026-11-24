package opencodego

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelsListing mirrors GET /models: each entry names the dialect it is served
// on in "api", which is what the provider routes by.
const modelsListing = `{"object":"list","data":[
	{"id":"muse-spark-1.3-contributor","object":"model","api":{"responses":{"base_url":"https://opencode.ai/inference/go/openai/v1"}}},
	{"id":"grok-4.7","object":"model","api":{"responses":{"base_url":"https://opencode.ai/inference/go/openai/v1"}}},
	{"id":"glm-5.1","object":"model","api":{"chat-completions":{"base_url":"https://opencode.ai/inference/go/openai/v1"}}},
	{"id":"hybrid-model","object":"model","api":{"responses":{},"chat-completions":{}}}
]}`

// responsesBody is a minimal completed Responses payload.
const responsesBody = `{
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

// chatBody is a minimal chat completion payload.
const chatBody = `{"id":"chatcmpl-1","created":1,"model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

// routedServer answers the model listing and both inference endpoints, so a
// test can assert which one the provider chose.
func routedServer(t *testing.T) (string, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, modelsListing)
		},
		"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, chatBody)
		},
		"/responses": func(w http.ResponseWriter, r *http.Request) {
			if wantsStream(r) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, responsesStreamBody)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responsesBody)
		},
	})
	return server.URL, capture
}

// wantsStream reports whether the recorded request asked for a stream: the
// provider sets it on the body, and a chat client may also send the Accept header.
func wantsStream(r *http.Request) bool {
	if r.Header.Get("Accept") == "text/event-stream" {
		return true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	var sent struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &sent) == nil && sent.Stream
}

func TestChatCompletion_ResponsesModel_UsesResponses(t *testing.T) {
	url, capture := routedServer(t)
	ctx := core.WithSessionID(context.Background(), "session-resp")

	_, err := newTestProvider(url, nil).ChatCompletion(ctx, chatRequest("muse-spark-1.3-contributor"))
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/responses", req.Path)
	assert.Equal(t, "session-resp", req.Header.Get("x-opencode-session"),
		"the session header must ride the native Responses dialect too")
	assert.Equal(t, "muse-spark-1.3-contributor", req.JSON(t)["model"])
}

// TestChatCompletion_ResponsesModel_AppliesReasoningHook pins the bypass the
// Responses route introduces: CompatibleProvider.ChatCompletion applies
// AdaptChatRequest, but ChatViaResponses does not, so the provider must. OpenCode
// Zen rejects requests that leave reasoning out and only accepts low/high/max.
func TestChatCompletion_ResponsesModel_AppliesReasoningHook(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), &core.ChatRequest{
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

// TestChatCompletion_ResponsesModel_NarrowsAllowedTools pins the other half of
// the bypass: CompatibleProvider narrows an allowed_tools choice before the
// upstream sees it, and the Responses translation rejects the choice outright, so
// the Responses route must narrow too instead of failing the request.
func TestChatCompletion_ResponsesModel_NarrowsAllowedTools(t *testing.T) {
	url, capture := routedServer(t)
	declared := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}},
		{"type": "function", "function": map[string]any{"name": "write", "parameters": map[string]any{"type": "object"}}},
	}

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "muse-spark-1.3-contributor",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools:    declared,
		ToolChoice: map[string]any{
			"type":          "allowed_tools",
			"allowed_tools": map[string]any{"mode": "auto", "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "read"}}}},
		},
	})
	require.NoError(t, err)

	body := capture.Last(t).JSON(t)
	tools, ok := body["tools"].([]any)
	require.True(t, ok, "missing tools in %v", body)
	require.Len(t, tools, 1)
	tool, _ := tools[0].(map[string]any)
	assert.Equal(t, "read", tool["name"], "the Responses dialect flattens the function object")
	assert.Equal(t, "auto", body["tool_choice"])
}

// TestChatCompletion_ResponsesModel_KeepsPlainToolChoice keeps a choice the
// translation already carries from being rewritten.
func TestChatCompletion_ResponsesModel_KeepsPlainToolChoice(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:      "muse-spark-1.3-contributor",
		Messages:   []core.Message{{Role: "user", Content: "hi"}},
		Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}},
		ToolChoice: "auto",
	})
	require.NoError(t, err)

	assert.Equal(t, "auto", capture.Last(t).JSON(t)["tool_choice"])
}

func TestChatCompletion_ChatModel_StaysOnChatCompletions(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), chatRequest("glm-5.1"))
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

// TestChatCompletion_ModelServedOnBothAPIs_StaysOnChat completes the rule: only
// a model whose responses endpoint is the one the listing publishes is routed
// there; a model also published on chat completions keeps that path.
func TestChatCompletion_ModelServedOnBothAPIs_StaysOnChat(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), chatRequest("hybrid-model"))
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

func TestStreamChatCompletion_ResponsesModel_UsesResponses(t *testing.T) {
	url, capture := routedServer(t)

	stream, err := newTestProvider(url, nil).StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "grok-4.7",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.NotEmpty(t, body, "the stream must carry the translated chat chunks")

	req := capture.Last(t)
	assert.Equal(t, "/responses", req.Path)
	assert.Equal(t, true, req.JSON(t)["stream"])
}

// TestResponses_ResponsesModel_UsesNativeEndpoint pins the mirror rule: a client
// that already speaks Responses must not be translated down to chat and back.
func TestResponses_ResponsesModel_UsesNativeEndpoint(t *testing.T) {
	url, capture := routedServer(t)

	resp, err := newTestProvider(url, nil).Responses(context.Background(), &core.ResponsesRequest{
		Model: "muse-spark-1.3-contributor",
		Input: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, "/responses", capture.Last(t).Path)
	require.Len(t, resp.Output, 1)
}

func TestStreamResponses_ResponsesModel_UsesNativeEndpoint(t *testing.T) {
	url, capture := routedServer(t)

	stream, err := newTestProvider(url, nil).StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "grok-4.7",
		Input: "hi",
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "response.completed")

	assert.Equal(t, "/responses", capture.Last(t).Path)
}

// TestChatCompletion_ResponsesModel_KeepsClientReasoningSummary pins the summary
// the chat adapter would otherwise lose: the chat dialect carries the effort as a
// flat reasoning_effort and has no nested reasoning object, so adaptChatRequest
// clears it, while the Responses translation reads the summary from that object
// and would replace an explicit one with "auto".
func TestChatCompletion_ResponsesModel_KeepsClientReasoningSummary(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "muse-spark-1.3-contributor",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "high", Summary: "detailed"},
	})
	require.NoError(t, err)

	body := capture.Last(t).JSON(t)
	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok, "missing reasoning object in %v", body)
	assert.Equal(t, "high", reasoning["effort"])
	assert.Equal(t, "detailed", reasoning["summary"], "an explicit summary must survive the chat-to-Responses translation")
	assert.NotContains(t, body, "reasoning_effort", "the Responses dialect carries the effort nested")
}

// TestResponses_ResponsesModel_AppliesReasoningPolicy pins the same policy on
// the native route: the client's effort is mapped onto the levels OpenCode Zen
// accepts, and a request that leaves reasoning out gets the configured default.
func TestResponses_ResponsesModel_AppliesReasoningPolicy(t *testing.T) {
	tests := []struct {
		name        string
		effort      string
		summary     string
		wantBody    string
		wantSummary string
	}{
		{name: "xhigh becomes max", effort: "xhigh", summary: "auto", wantBody: "max", wantSummary: "auto"},
		{name: "none becomes low", effort: "none", summary: "auto", wantBody: "low", wantSummary: "auto"},
		{name: "high passes through", effort: "high", summary: "auto", wantBody: "high", wantSummary: "auto"},
		{name: "an explicit summary is kept", effort: "high", summary: "detailed", wantBody: "high", wantSummary: "detailed"},
		{name: "absent gets the default", effort: "", wantBody: defaultReasoningEffort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, capture := routedServer(t)
			req := &core.ResponsesRequest{Model: "muse-spark-1.3-contributor", Input: "hi"}
			if tt.effort != "" {
				req.Reasoning = &core.Reasoning{Effort: tt.effort, Summary: tt.summary}
			}

			_, err := newTestProvider(url, nil).Responses(context.Background(), req)
			require.NoError(t, err)

			body := capture.Last(t).JSON(t)
			reasoning, ok := body["reasoning"].(map[string]any)
			require.True(t, ok, "missing reasoning object in %v", body)
			assert.Equal(t, tt.wantBody, reasoning["effort"])
			if tt.wantSummary != "" {
				assert.Equal(t, tt.wantSummary, reasoning["summary"], "the native route forwards the client's summary")
			}
		})
	}
}

// TestResponses_ResponsesModel_PromotesFlatReasoningEffort pins the chat-dialect
// member a Responses client may send: it is moved onto the nested reasoning object
// and dropped from the body, instead of reaching an endpoint that does not define
// it. Promoting it also keeps the default from overriding the client's choice.
func TestResponses_ResponsesModel_PromotesFlatReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		reasoning  *core.Reasoning
		flat       string
		wantEffort string
	}{
		{name: "flat effort is mapped", flat: `"xhigh"`, wantEffort: "max"},
		{name: "nested effort wins over flat", reasoning: &core.Reasoning{Effort: "high"}, flat: `"xhigh"`, wantEffort: "high"},
		{name: "flat effort stops the default", flat: `"high"`, wantEffort: "high"},
		{name: "invalid flat effort is dropped", flat: `null`, wantEffort: defaultReasoningEffort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(tt.flat),
			})
			require.NoError(t, err)
			url, capture := routedServer(t)

			_, err = newTestProvider(url, nil).Responses(context.Background(), &core.ResponsesRequest{
				Model:       "muse-spark-1.3-contributor",
				Input:       "hi",
				Reasoning:   tt.reasoning,
				ExtraFields: extra,
			})
			require.NoError(t, err)

			body := capture.Last(t).JSON(t)
			reasoning, ok := body["reasoning"].(map[string]any)
			require.True(t, ok, "missing reasoning object in %v", body)
			assert.Equal(t, tt.wantEffort, reasoning["effort"])
			assert.NotContains(t, body, "reasoning_effort", "the Responses dialect carries the effort nested")
		})
	}
}

// TestResponses_ResponsesModel_DefaultEffortCanBeDisabled keeps the operator's
// OPENCODE_GO_DEFAULT_REASONING_EFFORT=none from injecting anything on the
// native route either.
func TestResponses_ResponsesModel_DefaultEffortCanBeDisabled(t *testing.T) {
	t.Setenv(defaultReasoningEffortEnvVar, "none")
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).Responses(context.Background(), &core.ResponsesRequest{
		Model: "muse-spark-1.3-contributor",
		Input: "hi",
	})
	require.NoError(t, err)

	assert.NotContains(t, capture.Last(t).JSON(t), "reasoning")
}

func TestResponses_ChatModel_TranslatesToChat(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).Responses(context.Background(), &core.ResponsesRequest{
		Model: "glm-5.1",
		Input: "hi",
	})
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

// TestResponses_MessagesOverride_WinsOverNativeResponses pins the manual override:
// OPENCODE_GO_MESSAGES_MODELS keeps a model on the Anthropic-native endpoint even
// when the listing also publishes it on /responses, on both the buffered and the
// streaming route, mirroring what ChatCompletion already does.
func TestResponses_MessagesOverride_WinsOverNativeResponses(t *testing.T) {
	t.Setenv(messagesModelsEnvVar, "muse-spark-1.3-contributor")
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, modelsListing)
		},
		"/messages": func(w http.ResponseWriter, r *http.Request) {
			if wantsStream(r) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_1","model":"muse-spark-1.3-contributor","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
		},
		"/responses": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responsesBody)
		},
	})
	provider := newTestProvider(server.URL, nil)

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "muse-spark-1.3-contributor",
		Input: "hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "/messages", capture.Last(t).Path, "the override outranks the listing")

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "muse-spark-1.3-contributor",
		Input: "hi",
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_, _ = io.Copy(io.Discard, stream)

	assert.Equal(t, "/messages", capture.Last(t).Path, "streaming honors the override too")
}

// TestChatCompletion_UnknownModel_FallsBackToChat keeps a listing outage from
// failing traffic: a model the provider cannot place is sent where most OpenCode
// Zen models answer, as it was before routing existed.
func TestChatCompletion_UnknownModel_FallsBackToChat(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, chatBody)
		},
	})

	_, err := newTestProvider(server.URL, nil).ChatCompletion(context.Background(), chatRequest("glm-5.1"))
	require.NoError(t, err)

	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

// TestChatCompletion_RoutesListingIsCached pins the refetch discipline: a second
// request for a known model must not list models again, or every chat request
// would cost two upstream calls.
func TestChatCompletion_RoutesListingIsCached(t *testing.T) {
	url, capture := routedServer(t)
	provider := newTestProvider(url, nil)

	for range 2 {
		_, err := provider.ChatCompletion(context.Background(), chatRequest("glm-5.1"))
		require.NoError(t, err)
	}

	listings := 0
	for _, req := range capture.All() {
		if req.Path == "/models" {
			listings++
		}
	}
	assert.Equal(t, 1, listings, "the model listing must be cached between requests")
}

// TestChatCompletion_PrefixedModel_IsRouted keeps the provider-prefixed model
// IDs the router produces on the same route as the bare ones.
func TestChatCompletion_PrefixedModel_IsRouted(t *testing.T) {
	url, capture := routedServer(t)

	_, err := newTestProvider(url, nil).ChatCompletion(context.Background(), chatRequest("opencode_go/grok-4.7"))
	require.NoError(t, err)

	assert.Equal(t, "/responses", capture.Last(t).Path)
}

func TestBareModel_DropsProviderPrefix(t *testing.T) {
	assert.Equal(t, "grok-4.7", bareModel("opencode_go/grok-4.7"))
	assert.Equal(t, "grok-4.7", bareModel("grok-4.7"))
	assert.Empty(t, bareModel(""))
}
