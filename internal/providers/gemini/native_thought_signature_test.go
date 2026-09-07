package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

const testExtraContent = `{"google":{"thought_signature":"sig-1"}}`

func toolCallWithSignature(id, name, signature string) core.ToolCall {
	return core.ToolCall{
		ID:          id,
		Type:        "function",
		Function:    core.FunctionCall{Name: name, Arguments: `{"city":"Warsaw"}`},
		ExtraFields: thoughtSignatureExtraFields(signature),
	}
}

func rawFields(pairs map[string]string) core.UnknownJSONFields {
	fields := make(map[string]json.RawMessage, len(pairs))
	for key, value := range pairs {
		fields[key] = json.RawMessage(value)
	}
	return core.UnknownJSONFieldsFromMap(fields)
}

func TestConvertChatRequestToGemini_ThoughtSignatures(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		message  core.Message
		wantSigs []string // per part, in order
	}{
		{
			name:  "signed tool call is sent back verbatim",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", "sig-1"),
			}},
			wantSigs: []string{"sig-1"},
		},
		{
			name:  "unsigned tool call on Gemini 3 gets the validator placeholder",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", ""),
			}},
			wantSigs: []string{skipThoughtSignatureValidator},
		},
		{
			name:  "unsigned tool call on Gemini 2.5 stays unsigned",
			model: "gemini-2.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", ""),
			}},
			wantSigs: []string{""},
		},
		{
			name:  "parallel batch keeps only its first call signed",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", Content: "Checking both.", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", "sig-1"),
				toolCallWithSignature("call_2", "lookup_weather", ""),
			}},
			wantSigs: []string{"", "sig-1", ""},
		},
		{
			name:  "flat snake_case spelling on the tool call",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function:    core.FunctionCall{Name: "lookup_weather", Arguments: "{}"},
				ExtraFields: rawFields(map[string]string{"thought_signature": `"sig-flat"`}),
			}}},
			wantSigs: []string{"sig-flat"},
		},
		{
			name:  "flat camelCase spelling nested on the function object",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function: core.FunctionCall{
					Name: "lookup_weather", Arguments: "{}",
					ExtraFields: rawFields(map[string]string{"thoughtSignature": `"sig-func"`}),
				},
			}}},
			wantSigs: []string{"sig-func"},
		},
		{
			name:  "malformed extra_content falls back to the placeholder",
			model: "gemini-3.5-flash",
			message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function:    core.FunctionCall{Name: "lookup_weather", Arguments: "{}"},
				ExtraFields: rawFields(map[string]string{"extra_content": `{"google":"not-an-object"}`}),
			}}},
			wantSigs: []string{skipThoughtSignatureValidator},
		},
		{
			name:  "mixed turn keeps the text signature and the call signature",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "assistant",
				Content:     "Checking.",
				ExtraFields: thoughtSignatureExtraFields("sig-text"),
				ToolCalls:   []core.ToolCall{toolCallWithSignature("call_1", "lookup_weather", "sig-1")},
			},
			wantSigs: []string{"sig-text", "sig-1"},
		},
		{
			name:  "text turn signature lands on the last part",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "assistant",
				Content:     "Hello",
				ExtraFields: thoughtSignatureExtraFields("sig-text"),
			},
			wantSigs: []string{"sig-text"},
		},
		{
			name:  "user extra_content is ignored",
			model: "gemini-3.5-flash",
			message: core.Message{
				Role:        "user",
				Content:     "Hello",
				ExtraFields: thoughtSignatureExtraFields("sig-user"),
			},
			wantSigs: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := convertChatRequestToGemini(&core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "Weather?"}, tt.message},
			})
			if err != nil {
				t.Fatalf("convertChatRequestToGemini() error = %v", err)
			}
			parts := req.Contents[len(req.Contents)-1].Parts
			if len(parts) != len(tt.wantSigs) {
				t.Fatalf("parts = %d, want %d: %+v", len(parts), len(tt.wantSigs), parts)
			}
			for i, want := range tt.wantSigs {
				if got := parts[i].ThoughtSignature; got != want {
					t.Errorf("part %d thoughtSignature = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestNativeChatResponse_ExposesThoughtSignatures(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"text":"Let me check.","thoughtSignature":"sig-text"},
		{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"},
		{"functionCall":{"id":"call_2","name":"lookup_weather","args":{"city":"Krakow"}}}
	]},"finishReason":"STOP"}]}`), &geminiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3.5-flash"}, &geminiResp, "gemini")
	if err != nil {
		t.Fatalf("nativeChatResponse() error = %v", err)
	}
	encoded, err := json.Marshal(resp.Choices[0].Message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		ExtraContent json.RawMessage `json:"extra_content"`
		ToolCalls    []struct {
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal wire message: %v", err)
	}
	if got := string(wire.ExtraContent); got != `{"google":{"thought_signature":"sig-text"}}` {
		t.Fatalf("message extra_content = %s, want text signature", got)
	}
	if len(wire.ToolCalls) != 2 {
		t.Fatalf("tool_calls = %d, want 2", len(wire.ToolCalls))
	}
	if got := string(wire.ToolCalls[0].ExtraContent); got != testExtraContent {
		t.Fatalf("tool_calls[0].extra_content = %s, want %s", got, testExtraContent)
	}
	if got := string(wire.ToolCalls[1].ExtraContent); got != "" {
		t.Fatalf("tool_calls[1].extra_content = %s, want absent", got)
	}
}

// TestChatCompletion_NativeThoughtSignatureRoundTrip replays a Gemini tool
// call exactly as an OpenAI-compatible client would: the tool_calls from the
// first response, re-decoded from JSON, are sent back in the next request and
// must reach Gemini with their original thoughtSignature.
func TestChatCompletion_NativeThoughtSignatureRoundTrip(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		w.WriteHeader(http.StatusOK)
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}
			]},"finishReason":"STOP"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Sunny."}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	first, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather?"}},
	})
	if err != nil {
		t.Fatalf("first ChatCompletion() error = %v", err)
	}

	// Serialize the assistant message the way it leaves the gateway and decode
	// it the way the next request arrives.
	encoded, err := json.Marshal(first.Choices[0].Message)
	if err != nil {
		t.Fatalf("marshal assistant message: %v", err)
	}
	var assistant core.Message
	if err := json.Unmarshal(encoded, &assistant); err != nil {
		t.Fatalf("unmarshal assistant message: %v", err)
	}

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Weather?"},
			assistant,
			{Role: "tool", ToolCallID: "call_1", Content: `{"result":"sunny"}`},
		},
	}); err != nil {
		t.Fatalf("second ChatCompletion() error = %v", err)
	}

	var payload geminiGenerateContentRequest
	if err := json.Unmarshal(requests[1], &payload); err != nil {
		t.Fatalf("unmarshal second request: %v", err)
	}
	if len(payload.Contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(payload.Contents))
	}
	call := payload.Contents[1].Parts[0]
	if call.FunctionCall == nil || call.FunctionCall.Name != "lookup_weather" {
		t.Fatalf("model part = %+v, want functionCall", call)
	}
	if call.ThoughtSignature != "sig-1" {
		t.Fatalf("thoughtSignature = %q, want sig-1", call.ThoughtSignature)
	}
}

func TestStreamChatCompletion_NativeThoughtSignatures(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig-text"}]},"finishReason":"STOP"}]}

`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, `"tool_calls":[{"extra_content":{"google":{"thought_signature":"sig-1"}}`) {
		t.Fatalf("stream lacks tool call extra_content: %s", stream)
	}
	if !strings.Contains(stream, `"delta":{"extra_content":{"google":{"thought_signature":"sig-text"}}}`) {
		t.Fatalf("stream lacks text turn extra_content: %s", stream)
	}
}

func TestNativeChatResponse_ToolCallOnlyTurnHasNullContent(t *testing.T) {
	var geminiResp geminiGenerateContentResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"role":"model","parts":[
		{"functionCall":{"id":"call_1","name":"lookup_weather","args":{}}}
	]},"finishReason":"STOP"}]}`), &geminiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3.5-flash"}, &geminiResp, "gemini")
	if err != nil {
		t.Fatalf("nativeChatResponse() error = %v", err)
	}
	if got := resp.Choices[0].Message.Content; got != nil {
		t.Fatalf("content = %#v, want nil for a tool-call-only turn", got)
	}
}
