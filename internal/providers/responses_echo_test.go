package providers

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func echoRequest() *core.ResponsesRequest {
	temperature := 0.25
	topP := 0.9
	maxOutputTokens := 32
	parallel := false
	store := true
	return &core.ResponsesRequest{
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
				"truncation", "user", "service_tier",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			echo := ResponsesRequestEcho(tt.req)
			if len(echo) != len(tt.want) {
				t.Fatalf("echo keys = %v, want %v", echo, tt.want)
			}
			for _, name := range tt.want {
				if len(echo[name]) == 0 {
					t.Fatalf("echo[%q] missing from %v", name, echo)
				}
			}
		})
	}
}

// truncation is an enum: the echo carries the canonical spelling the validator
// accepted, not the caller's padding, so a strict client still sees a legal value.
func TestResponsesRequestEchoCanonicalizesTruncation(t *testing.T) {
	req := &core.ResponsesRequest{Model: "m", Input: "hi", Truncation: " disabled "}

	if got := string(ResponsesRequestEcho(req)["truncation"]); got != `"disabled"` {
		t.Fatalf("truncation = %s, want \"disabled\"", got)
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

	if got := string(resp.ExtraFields.Lookup("instructions")); got != `"from provider"` {
		t.Fatalf("instructions = %s, want the provider value", got)
	}
	if len(resp.ExtraFields.Lookup("metadata")) == 0 {
		t.Fatal("metadata was not echoed alongside the provider member")
	}
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
	if err != nil {
		t.Fatalf("ResponsesViaChat() error = %v", err)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var nonStream map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &nonStream); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	stream, err := StreamResponsesViaChat(context.Background(), provider, req.WithStreaming(), "anthropic")
	if err != nil {
		t.Fatalf("StreamResponsesViaChat() error = %v", err)
	}
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	if err != nil && err != io.EOF {
		t.Fatalf("read stream: %v", err)
	}
	streamed := lastLifecycleResponse(t, string(body))

	for name := range ResponsesRequestEcho(req) {
		if len(nonStream[name]) == 0 {
			t.Fatalf("non-streaming response is missing %q: %s", name, encoded)
		}
		if string(nonStream[name]) != string(streamed[name]) {
			t.Fatalf("%q = %s non-streaming, %s streaming", name, nonStream[name], streamed[name])
		}
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
	if last == nil {
		t.Fatalf("no lifecycle event found in stream: %s", body)
	}
	return last
}
