package streaming

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func chatFixtureResponses() map[string]*core.ChatResponse {
	return map[string]*core.ChatResponse{
		"text": {
			ID: "chatcmpl-1", Object: "chat.completion", Model: "gpt-4o", Provider: "openai", SystemFingerprint: "fp_1", Created: 1700000000,
			Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "Hello \"world\"\nline two"}, FinishReason: "stop"}},
			Usage:   core.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
		},
		"tool call": {
			ID: "chatcmpl-2", Object: "chat.completion", Model: "gpt-4o", Provider: "openai", Created: 1700000001,
			Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", ToolCalls: []core.ToolCall{
				{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
				{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "get_time", Arguments: `{}`}},
			}}, FinishReason: "tool_calls"}},
			Usage: core.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		},
		"multi choice with reasoning": {
			ID: "chatcmpl-3", Object: "chat.completion", Model: "r1", Provider: "deepseek", Created: 1700000002,
			Choices: []core.Choice{
				{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "A", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_content": json.RawMessage(`"because"`)})}, FinishReason: "stop"},
				{Index: 1, Message: core.ResponseMessage{Role: "assistant", Content: "B"}, FinishReason: "length"},
			},
			Usage: core.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		},
	}
}

func TestChatStreamRoundTrip(t *testing.T) {
	for name, resp := range chatFixtureResponses() {
		t.Run(name, func(t *testing.T) {
			stream := SynthesizeChatStream(resp, true)
			if !strings.HasSuffix(string(stream), "data: [DONE]\n\n") {
				t.Fatalf("stream must end with [DONE]:\n%s", stream)
			}
			got, err := AssembleChatResponse(decodeChatEvents(t, stream))
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(resp)
			gotJSON, _ := json.Marshal(got)
			if !reflect.DeepEqual(decodeMap(t, wantJSON), decodeMap(t, gotJSON)) {
				t.Errorf("round trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestSynthesizeChatStream_Shape(t *testing.T) {
	resp := chatFixtureResponses()["tool call"]
	events := decodeStreamEvents(t, SynthesizeChatStream(resp, false))
	// role, two tool calls, finish; no usage chunk.
	if len(events) != 4 {
		t.Fatalf("got %d chunks, want 4:\n%s", len(events), SynthesizeChatStream(resp, false))
	}
	for i, ev := range events {
		if ev["object"] != "chat.completion.chunk" || ev["id"] != "chatcmpl-2" || ev["provider"] != "openai" {
			t.Errorf("chunk %d envelope = %v", i, ev)
		}
		if _, ok := ev["usage"]; ok {
			t.Errorf("chunk %d carries usage without include_usage", i)
		}
	}
	first := events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if first["role"] != "assistant" {
		t.Errorf("first chunk delta = %v", first)
	}
	last := events[3]["choices"].([]any)[0].(map[string]any)
	if last["finish_reason"] != "tool_calls" {
		t.Errorf("last chunk = %v", last)
	}
}

func TestAssembleChatResponse_ProviderShapes(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		want    string
		finish  string
		wantErr bool
	}{
		{
			name: "content with finish on the same chunk and missing index",
			stream: `data: {"id":"x","choices":[{"delta":{"content":"a"}}]}` + "\n\n" +
				`data: {"id":"x","choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}` + "\n\n",
			want:   "ab",
			finish: "stop",
		},
		{
			name:    "no decodable payload",
			stream:  "data: [DONE]\n\n",
			wantErr: true,
		},
		{
			name:   "unparseable chunks are skipped",
			stream: "data: nope\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n",
			want:   "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := AssembleChatResponse(decodeChatEvents(t, []byte(tt.stream)))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Choices[0].Message.Content != tt.want || resp.Choices[0].FinishReason != tt.finish {
				t.Errorf("choice = %+v", resp.Choices[0])
			}
		})
	}
}

func responsesFixtures() map[string]*core.ResponsesResponse {
	return map[string]*core.ResponsesResponse{
		"text": {
			ID: "resp_1", Object: "response", CreatedAt: 1700000000, Model: "gpt-4.1", Provider: "openai", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Status: "completed",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "Hello there"}}}},
			Usage: &core.ResponsesUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		},
		"function call and reasoning": {
			ID: "resp_2", Object: "response", CreatedAt: 1700000001, Model: "o4-mini", Provider: "openai", Status: "completed",
			Output: []core.ResponsesOutputItem{
				{ID: "rs_1", Type: "reasoning", Status: "completed", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"summary": json.RawMessage(`[]`)})},
				{ID: "fc_1", Type: "function_call", Status: "completed", CallID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`},
			},
		},
		"incomplete": {
			ID: "resp_3", Object: "response", CreatedAt: 1, Model: "m", Provider: "p", Status: "incomplete",
			Output: []core.ResponsesOutputItem{{ID: "msg", Type: "message", Role: "assistant", Status: "incomplete",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "partial"}}}},
		},
	}
}

func TestResponsesStreamRoundTrip(t *testing.T) {
	for name, resp := range responsesFixtures() {
		t.Run(name, func(t *testing.T) {
			stream := SynthesizeResponsesStream(resp)
			got, err := AssembleResponsesResponse(decodeResponsesEvents(t, stream))
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(resp)
			gotJSON, _ := json.Marshal(got)
			if !reflect.DeepEqual(decodeMap(t, wantJSON), decodeMap(t, gotJSON)) {
				t.Errorf("round trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestSynthesizeResponsesStream_Shape(t *testing.T) {
	resp := responsesFixtures()["text"]
	stream := SynthesizeResponsesStream(resp)
	if !strings.HasSuffix(string(stream), "data: [DONE]\n\n") {
		t.Fatalf("stream must end with [DONE]:\n%s", stream)
	}
	events := decodeStreamEvents(t, stream)
	var types []string
	for i, ev := range events {
		types = append(types, ev["type"].(string))
		if ev["sequence_number"] != float64(i) {
			t.Errorf("event %d sequence_number = %v", i, ev["sequence_number"])
		}
	}
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done",
		"response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if !equalStrings(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
	if events[4]["delta"] != "Hello there" {
		t.Errorf("delta event = %v", events[4])
	}
	created := events[0]["response"].(map[string]any)
	if created["status"] != "in_progress" || len(created["output"].([]any)) != 0 {
		t.Errorf("response.created = %v", created)
	}
	for _, raw := range scanAll(t, &EventScanner{}, string(stream)) {
		if !raw.Comment && string(raw.Data) != "[DONE]" && raw.Name == "" {
			t.Errorf("event without event: line: %s", raw.Raw)
		}
	}
}

func TestAssembleResponsesResponse_FromDeltasOnly(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		text   string
		status string
	}{
		{
			name: "items without a terminal event",
			stream: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\",\"created_at\":5}}\n\n" +
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ab\"}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"cd\"}\n\n",
			text:   "abcd",
			status: "incomplete",
		},
		{
			name:   "bare deltas become one message item",
			stream: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"y\"}\n\n",
			text:   "xy",
			status: "incomplete",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, []byte(tt.stream)))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tt.status || len(resp.Output) != 1 || resp.Output[0].Content[0].Text != tt.text {
				t.Errorf("assembled = %+v", resp)
			}
		})
	}
	if _, err := AssembleResponsesResponse(nil); err != ErrNoEvents {
		t.Errorf("empty assemble err = %v", err)
	}
}
