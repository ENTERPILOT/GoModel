package anthropicapi

import (
	"encoding/json"
	"testing"

	"gomodel/internal/core"
)

func TestFromChatResponseText(t *testing.T) {
	resp := FromChatResponse(&core.ChatResponse{
		ID:    "abc123",
		Model: "claude-test",
		Choices: []core.Choice{{
			Message:      core.ResponseMessage{Role: "assistant", Content: "hello there"},
			FinishReason: "stop",
		}},
		Usage: core.Usage{PromptTokens: 12, CompletionTokens: 7},
	})
	if resp.ID != "msg_abc123" {
		t.Errorf("ID = %q, want msg_abc123", resp.ID)
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("envelope = %+v", resp)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "hello there" {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestFromChatResponseToolCalls(t *testing.T) {
	resp := FromChatResponse(&core.ChatResponse{
		ID:    "msg_x",
		Model: "m",
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role: "assistant",
				ToolCalls: []core.ToolCall{{
					ID:       "tu_1",
					Type:     "function",
					Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"paris"}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
	})
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" {
		t.Fatalf("content = %+v", resp.Content)
	}
	block := resp.Content[0]
	if block.ID != "tu_1" || block.Name != "get_weather" {
		t.Errorf("tool_use block = %+v", block)
	}
	if string(block.Input) != `{"city":"paris"}` {
		t.Errorf("input = %s", block.Input)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
}

func TestFromChatResponseThinking(t *testing.T) {
	thinking, _ := json.Marshal("let me think")
	resp := FromChatResponse(&core.ChatResponse{
		ID:    "msg_x",
		Model: "m",
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role:    "assistant",
				Content: "answer",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_content": thinking,
				}),
			},
			FinishReason: "stop",
		}},
	})
	if len(resp.Content) != 2 {
		t.Fatalf("content = %+v, want thinking + text", resp.Content)
	}
	if resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "let me think" {
		t.Errorf("thinking block = %+v", resp.Content[0])
	}
	if resp.Content[1].Type != "text" {
		t.Errorf("text block = %+v", resp.Content[1])
	}
}

func TestFromChatResponseStopReasons(t *testing.T) {
	tests := []struct {
		finish string
		want   string
	}{
		{finish: "stop", want: "end_turn"},
		{finish: "length", want: "max_tokens"},
		{finish: "tool_calls", want: "tool_use"},
		{finish: "content_filter", want: "end_turn"},
		{finish: "", want: "end_turn"},
	}
	for _, tc := range tests {
		t.Run(tc.finish, func(t *testing.T) {
			resp := FromChatResponse(&core.ChatResponse{
				Choices: []core.Choice{{
					Message:      core.ResponseMessage{Content: "x"},
					FinishReason: tc.finish,
				}},
			})
			if resp.StopReason != tc.want {
				t.Errorf("StopReason = %q, want %q", resp.StopReason, tc.want)
			}
		})
	}
}

func TestFromChatResponseCacheUsage(t *testing.T) {
	resp := FromChatResponse(&core.ChatResponse{
		Usage: core.Usage{
			PromptTokens:     100,
			CompletionTokens: 20,
			RawUsage: map[string]any{
				"cache_creation_input_tokens": float64(30),
				"cache_read_input_tokens":     float64(40),
			},
		},
	})
	if resp.Usage.CacheCreationInputTokens != 30 || resp.Usage.CacheReadInputTokens != 40 {
		t.Errorf("cache usage = %+v", resp.Usage)
	}
}

func TestFromChatResponseNil(t *testing.T) {
	resp := FromChatResponse(nil)
	if resp == nil || resp.Type != "message" || resp.Content == nil {
		t.Fatalf("FromChatResponse(nil) = %+v", resp)
	}
}
