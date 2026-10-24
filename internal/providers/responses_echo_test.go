package providers

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func echoRequest() *core.ResponsesRequest {
	temperature := 0.25
	topP := 0.9
	maxOutputTokens := 32
	parallel := false
	store := true
	return &core.ResponsesRequest{
		// Members the gateway does not model reach the echo through ExtraFields.
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"frequency_penalty": json.RawMessage(`0.5`),
			"presence_penalty":  json.RawMessage(`0.25`),
			"max_tool_calls":    json.RawMessage(`4`),
			"prompt_cache_key":  json.RawMessage(`"key-1"`),
			"background":        json.RawMessage(`false`),
		}),
		Model:             "claude-haiku-4-5",
		Input:             "hi",
		Instructions:      "be terse",
		Metadata:          map[string]string{"k": "v"},
		Tools:             []map[string]any{{"type": "function", "name": "ping", "parameters": map[string]any{}}},
		ToolChoice:        "auto",
		ParallelToolCalls: &parallel,
		Temperature:       &temperature,
		TopP:              &topP,
		MaxOutputTokens:   &maxOutputTokens,
		Store:             &store,
		Text:              map[string]any{"format": map[string]any{"type": "text"}},
		Reasoning:         &core.Reasoning{Effort: "low"},
		Truncation:        "disabled",
		User:              "user-1",
		ServiceTier:       "auto",
	}
}

// The echoed members are exactly the request fields OpenAI repeats on the
// Response object, and only the ones the caller supplied.
func TestResponsesRequestEcho(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ResponsesRequest
		want []string
	}{
		{name: "nil request"},
		{name: "bare request", req: &core.ResponsesRequest{Model: "m", Input: "hi"}},
		{
			name: "every echoable field",
			req:  echoRequest(),
			want: []string{
				"instructions", "metadata", "tools", "tool_choice", "parallel_tool_calls",
				"temperature", "top_p", "max_output_tokens", "store", "text", "reasoning",
				"truncation", "user", "service_tier", "frequency_penalty",
				"presence_penalty", "max_tool_calls", "prompt_cache_key", "background",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			echo := ResponsesRequestEcho(tt.req)
			require.Len(t, echo, len(tt.want), "echo keys = %v, want %v", echo, tt.want)
			for _, name := range tt.want {
				assert.NotEmpty(t, echo[name], "echo[%q] missing from %v", name, echo)
			}
		})
	}
}

// truncation is an enum: the echo carries the canonical spelling the validator
// accepted, not the caller's padding, so a strict client still sees a legal value.
func TestResponsesRequestEchoCanonicalizesTruncation(t *testing.T) {
	req := &core.ResponsesRequest{Model: "m", Input: "hi", Truncation: " disabled "}

	assert.Equal(t, `"disabled"`, string(ResponsesRequestEcho(req)["truncation"]))
}

// Members the gateway does not model are decoded onto ExtraFields, and the
// ones OpenAI repeats on the Response object are echoed from there. Routing
// hints and anything else the caller sent are not: OpenAI does not echo them.
func TestResponsesRequestEchoFromDecodedExtraFields(t *testing.T) {
	body := `{"model":"anthropic/claude-haiku-4-5","input":"hi","stream":false,` +
		`"frequency_penalty":0.5,"presence_penalty":0.25,"max_tool_calls":4,` +
		`"prompt_cache_key":"key-1","background":false,"x_client_trace":"t-1"}`
	var req core.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	echo := ResponsesRequestEcho(&req)
	want := map[string]string{
		"frequency_penalty": "0.5",
		"presence_penalty":  "0.25",
		"max_tool_calls":    "4",
		"prompt_cache_key":  `"key-1"`,
		"background":        "false",
	}
	for name, value := range want {
		assert.Equal(t, value, string(echo[name]), "echo[%q]", name)
	}
	for _, name := range []string{"x_client_trace", "stream", "model", "input"} {
		assert.NotContains(t, echo, name)
	}
}

// A member the provider itself returned must win over the echoed request value.
func TestApplyResponsesRequestEchoKeepsProviderMembers(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID: "resp_1",
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"instructions": json.RawMessage(`"from provider"`),
		}),
	}

	ApplyResponsesRequestEcho(resp, echoRequest())

	assert.Equal(t, `"from provider"`, string(resp.ExtraFields.Lookup("instructions")))
	assert.NotEmpty(t, resp.ExtraFields.Lookup("metadata"), "metadata was not echoed alongside the provider member")
}

// A chat-translated provider answers with the same echoed members whether the
// caller streamed the request or not.
func TestResponsesViaChatEchoMatchesStream(t *testing.T) {
	req := echoRequest()
	provider := &capturingChatProvider{
		chatResp: &core.ChatResponse{
			ID:      "chatcmpl-1",
			Model:   req.Model,
			Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
		},
		streamData: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	}

	resp, err := ResponsesViaChat(context.Background(), provider, req, "anthropic")
	require.NoError(t, err)
	encoded, err := json.Marshal(resp)
	require.NoError(t, err)
	var nonStream map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &nonStream))

	stream, err := StreamResponsesViaChat(context.Background(), provider, req.WithStreaming(), "anthropic")
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	streamed := lastLifecycleResponse(t, string(body))

	for name := range ResponsesRequestEcho(req) {
		require.NotEmpty(t, nonStream[name], "non-streaming response is missing %q: %s", name, encoded)
		assert.Equal(t, string(nonStream[name]), string(streamed[name]), "%q non-streaming vs streaming", name)
	}
}

// lastLifecycleResponse returns the response object of the final
// response-carrying SSE event in an SSE body.
func lastLifecycleResponse(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var last map[string]json.RawMessage
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var event struct {
			Response map[string]json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Response != nil {
			last = event.Response
		}
	}
	require.NotNil(t, last, "no lifecycle event found in stream: %s", body)
	return last
}
