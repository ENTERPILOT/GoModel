package gemini

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// signatureFromResponseToolCall reads the signature back off a tool call the
// way a client sees it: through the marshaled OpenAI-compatible JSON.
func signatureFromResponseToolCall(t *testing.T, call core.ToolCall) string {
	t.Helper()
	body, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("Marshal(tool call) error = %v", err)
	}
	var decoded struct {
		ExtraContent struct {
			Google struct {
				ThoughtSignature string `json:"thought_signature"`
			} `json:"google"`
		} `json:"extra_content"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", body, err)
	}
	return decoded.ExtraContent.Google.ThoughtSignature
}

func TestOpenAIMessageFromGeminiParts_KeepsThoughtSignature(t *testing.T) {
	tests := []struct {
		name  string
		parts []geminiPart
		want  []string
	}{
		{
			name: "single call",
			parts: []geminiPart{
				{Text: "looking that up"},
				{FunctionCall: &geminiFunctionCall{Name: "ExecCommand", Args: json.RawMessage(`{"cmd":"ls"}`)}, ThoughtSignature: "sig-1"},
			},
			want: []string{"sig-1"},
		},
		{
			name: "parallel calls carry the signature only on the first",
			parts: []geminiPart{
				{FunctionCallAlt: &geminiFunctionCall{Name: "ExecCommand"}, ThoughtSignature: "sig-1"},
				{FunctionCallAlt: &geminiFunctionCall{Name: "ReadFile"}},
			},
			want: []string{"sig-1", ""},
		},
		{
			name:  "call without a signature stays plain",
			parts: []geminiPart{{FunctionCall: &geminiFunctionCall{Name: "ExecCommand"}}},
			want:  []string{""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, toolCalls := openAIMessageFromGeminiParts(tc.parts)
			if len(toolCalls) != len(tc.want) {
				t.Fatalf("tool calls = %d, want %d", len(toolCalls), len(tc.want))
			}
			for i, want := range tc.want {
				if got := signatureFromResponseToolCall(t, toolCalls[i]); got != want {
					t.Fatalf("tool call %d signature = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestOpenAIMessageFromGeminiParts_UnsignedToolCallOmitsExtraContent(t *testing.T) {
	_, toolCalls := openAIMessageFromGeminiParts([]geminiPart{
		{FunctionCall: &geminiFunctionCall{ID: "call_1", Name: "ExecCommand", Args: json.RawMessage(`{}`)}},
	})
	body, err := json.Marshal(toolCalls[0])
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(body), "extra_content") {
		t.Fatalf("tool call JSON = %s, want no extra_content member", body)
	}
}

// A client replaying the gateway's own assistant message must reach Gemini
// with the signature back on the functionCall part it was returned on;
// Gemini 3 answers HTTP 400 otherwise.
func TestConvertChatRequestToGemini_RestoresThoughtSignature(t *testing.T) {
	tests := []struct {
		name     string
		toolCall string
		want     string
	}{
		{
			name:     "extra_content spelling",
			toolCall: `{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{\"cmd\":\"ls\"}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}`,
			want:     "sig-1",
		},
		{
			name:     "flat snake_case spelling",
			toolCall: `{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{}"},"thought_signature":"sig-1"}`,
			want:     "sig-1",
		},
		{
			name:     "flat camelCase spelling",
			toolCall: `{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{}"},"thoughtSignature":"sig-1"}`,
			want:     "sig-1",
		},
		{
			name:     "signature nested on the function object",
			toolCall: `{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{}","thought_signature":"sig-1"}}`,
			want:     "sig-1",
		},
		{
			name:     "no signature",
			toolCall: `{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{}"}}`,
			want:     "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req core.ChatRequest
			body := `{"model":"gemini-3-pro-preview","messages":[` +
				`{"role":"user","content":"list the files"},` +
				`{"role":"assistant","content":null,"tool_calls":[` + tc.toolCall + `]},` +
				`{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			out, err := convertChatRequestToGemini(&req)
			if err != nil {
				t.Fatalf("convertChatRequestToGemini() error = %v", err)
			}
			if len(out.Contents) != 3 {
				t.Fatalf("contents = %d, want 3", len(out.Contents))
			}
			model := out.Contents[1]
			if len(model.Parts) != 1 || model.Parts[0].FunctionCall == nil {
				t.Fatalf("model parts = %#v, want one functionCall part", model.Parts)
			}
			if got := model.Parts[0].ThoughtSignature; got != tc.want {
				t.Fatalf("thoughtSignature = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConvertChatRequestToGemini_ThoughtSignatureOnTheWire(t *testing.T) {
	var req core.ChatRequest
	body := `{"model":"gemini-3-pro-preview","messages":[` +
		`{"role":"user","content":"list the files"},` +
		`{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"call_1","type":"function","function":{"name":"ExecCommand","arguments":"{}"},` +
		`"extra_content":{"google":{"thought_signature":"sig-1"}}}]}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	out, err := convertChatRequestToGemini(&req)
	if err != nil {
		t.Fatalf("convertChatRequestToGemini() error = %v", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"thoughtSignature":"sig-1"`) {
		t.Fatalf("request body = %s, want thoughtSignature next to the functionCall", encoded)
	}
}

// The gateway's own response must replay cleanly: convert a Gemini answer to
// the OpenAI shape, hand it back as history, and the signature must land on
// the same part it arrived on.
func TestGeminiThoughtSignatureRoundTrip(t *testing.T) {
	upstream := `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"ExecCommand","args":{"cmd":"ls"}},"thoughtSignature":"sig-1"},` +
		`{"functionCall":{"name":"ReadFile","args":{"path":"go.mod"}}}` +
		`]},"finishReason":"STOP"}]}`
	var geminiResp geminiGenerateContentResponse
	if err := json.Unmarshal([]byte(upstream), &geminiResp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	resp, err := nativeChatResponse(&core.ChatRequest{Model: "gemini-3-pro-preview"}, &geminiResp, "gemini")
	if err != nil {
		t.Fatalf("nativeChatResponse() error = %v", err)
	}

	// The client echoes the assistant message back as chat history.
	assistant, err := json.Marshal(resp.Choices[0].Message)
	if err != nil {
		t.Fatalf("Marshal(message) error = %v", err)
	}
	var replay core.ChatRequest
	replayBody := `{"model":"gemini-3-pro-preview","messages":[{"role":"user","content":"list the files"},` +
		string(assistant) + `]}`
	if err := json.Unmarshal([]byte(replayBody), &replay); err != nil {
		t.Fatalf("Unmarshal(replay) error = %v", err)
	}

	out, err := convertChatRequestToGemini(&replay)
	if err != nil {
		t.Fatalf("convertChatRequestToGemini() error = %v", err)
	}
	parts := out.Contents[1].Parts
	if len(parts) != 2 {
		t.Fatalf("model parts = %#v, want two functionCall parts", parts)
	}
	if parts[0].ThoughtSignature != "sig-1" {
		t.Fatalf("first part signature = %q, want %q", parts[0].ThoughtSignature, "sig-1")
	}
	if parts[1].ThoughtSignature != "" {
		t.Fatalf("second part signature = %q, want none", parts[1].ThoughtSignature)
	}
}

func TestGeminiNativeStream_KeepsThoughtSignature(t *testing.T) {
	upstream := "data: " + `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"ExecCommand","args":{"cmd":"ls"}},"thoughtSignature":"sig-1"}` +
		`]},"finishReason":"STOP","index":0}],"responseId":"resp-1"}` + "\n\n"

	stream := newGeminiNativeStream(io.NopCloser(strings.NewReader(upstream)), "gemini-3-pro-preview", false, "gemini")
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(raw), `"extra_content":{"google":{"thought_signature":"sig-1"}}`) {
		t.Fatalf("stream = %s, want the tool call delta to carry extra_content", raw)
	}
}
