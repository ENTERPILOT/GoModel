package providers

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

const thoughtSignatureExtra = `{"google":{"thought_signature":"sig-1"}}`

// Gemini's function-call thought signature has to survive the chat/Responses
// translation: a Responses client replays function_call items, and Gemini 3
// answers HTTP 400 when a replayed functionCall part has lost its signature.
func TestBuildResponsesOutputItems_KeepsToolCallExtraContent(t *testing.T) {
	items := BuildResponsesOutputItems(core.ResponseMessage{
		Role: "assistant",
		ToolCalls: []core.ToolCall{
			{
				ID:       "call_1",
				Type:     "function",
				Function: core.FunctionCall{Name: "ExecCommand", Arguments: `{"cmd":"ls"}`},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"extra_content": json.RawMessage(thoughtSignatureExtra),
				}),
			},
			{
				ID:       "call_2",
				Type:     "function",
				Function: core.FunctionCall{Name: "ReadFile", Arguments: `{}`},
			},
		},
	})

	if len(items) != 2 {
		t.Fatalf("output items = %#v, want two function_call items", items)
	}
	signed, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(signed), `"extra_content":`+thoughtSignatureExtra) {
		t.Fatalf("function_call item = %s, want extra_content preserved", signed)
	}
	unsigned, err := json.Marshal(items[1])
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(unsigned), "extra_content") {
		t.Fatalf("function_call item = %s, want no extra_content member", unsigned)
	}
}

func TestToolCallExtraContent_DropsUnrelatedMembers(t *testing.T) {
	fields := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"extra_content": json.RawMessage(thoughtSignatureExtra),
		"status":        json.RawMessage(`"completed"`),
	})

	out := ToolCallExtraContent(fields)
	if out.Lookup("status") != nil {
		t.Fatalf("extras = %v, want item metadata dropped", out)
	}
	if string(out.Lookup("extra_content")) != thoughtSignatureExtra {
		t.Fatalf("extra_content = %s, want %s", out.Lookup("extra_content"), thoughtSignatureExtra)
	}
}

// A Responses client sends the signed function_call item back as input; the
// signature must reach the provider on the chat tool call.
func TestConvertResponsesToChat_KeepsFunctionCallExtraContent(t *testing.T) {
	inputs := []any{
		map[string]any{"type": "message", "role": "user", "content": "list the files"},
		map[string]any{
			"type":          "function_call",
			"call_id":       "call_1",
			"name":          "ExecCommand",
			"arguments":     `{"cmd":"ls"}`,
			"extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig-1"}},
		},
	}
	for _, name := range []string{"map input", "typed input"} {
		t.Run(name, func(t *testing.T) {
			input := any(inputs)
			if name == "typed input" {
				encoded, err := json.Marshal(inputs)
				if err != nil {
					t.Fatalf("Marshal() error = %v", err)
				}
				var elements []core.ResponsesInputElement
				if err := json.Unmarshal(encoded, &elements); err != nil {
					t.Fatalf("Unmarshal() error = %v", err)
				}
				input = elements
			}

			chat, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{Model: "gemini-3-pro-preview", Input: input})
			if err != nil {
				t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
			}
			var toolCall *core.ToolCall
			for i := range chat.Messages {
				if len(chat.Messages[i].ToolCalls) > 0 {
					toolCall = &chat.Messages[i].ToolCalls[0]
				}
			}
			if toolCall == nil {
				t.Fatalf("messages = %#v, want an assistant tool call", chat.Messages)
			}
			if got := string(toolCall.ExtraFields.Lookup("extra_content")); got != thoughtSignatureExtra {
				t.Fatalf("extra_content = %s, want %s", got, thoughtSignatureExtra)
			}
		})
	}
}

// The streamed Responses surface emits the same function_call items, so the
// signature has to ride along there too.
func TestResponsesStreamConverter_KeepsToolCallExtraContent(t *testing.T) {
	chunks := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function",` +
			`"function":{"name":"ExecCommand","arguments":"{\"cmd\":\"ls\"}"},` +
			`"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"data: [DONE]",
		"",
	}, "\n\n")

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(chunks)), "gemini-3-pro-preview", "gemini")
	out, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if count := strings.Count(string(out), `"extra_content":{"google":{"thought_signature":"sig-1"}}`); count == 0 {
		t.Fatalf("stream = %s, want function_call items to carry extra_content", out)
	}
}
