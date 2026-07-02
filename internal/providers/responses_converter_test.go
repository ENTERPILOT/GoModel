package providers

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type testSSEEvent struct {
	Name    string
	Payload map[string]any
	Done    bool
}

func TestOpenAIResponsesStreamConverter_WithToolCalls(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"War"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"saw\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := NewOpenAIResponsesStreamConverter(reader, "test-model", "groq")

	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundAdded := false
	foundArgumentsDone := false
	foundItemDone := false
	var argumentDeltas []string

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" && item["call_id"] == "call_123" && item["name"] == "lookup_weather" {
				foundAdded = true
			}
		case "response.function_call_arguments.delta":
			if delta, _ := event.Payload["delta"].(string); delta != "" {
				argumentDeltas = append(argumentDeltas, delta)
			}
		case "response.function_call_arguments.done":
			if event.Payload["arguments"] == `{"city":"Warsaw"}` {
				foundArgumentsDone = true
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" && item["arguments"] == `{"city":"Warsaw"}` {
				foundItemDone = true
			}
		}
	}

	if !foundAdded {
		t.Fatal("expected response.output_item.added for function_call")
	}
	if len(argumentDeltas) != 2 || argumentDeltas[0] != "{\"city\":\"War" || argumentDeltas[1] != "saw\"}" {
		t.Fatalf("response.function_call_arguments.delta sequence = %#v, want two ordered fragments", argumentDeltas)
	}
	if !foundArgumentsDone {
		t.Fatal("expected response.function_call_arguments.done for function_call")
	}
	if !foundItemDone {
		t.Fatal("expected response.output_item.done for function_call")
	}
}

func TestOpenAIResponsesStreamConverter_WithTextBeforeToolCall(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"content":"I'll check that for you."},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := NewOpenAIResponsesStreamConverter(reader, "test-model", "groq")

	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundTextDelta := false
	foundAssistantAdded := false
	foundAssistantDone := false
	foundToolAddedAtIndexOne := false

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "message" && item["role"] == "assistant" && event.Payload["output_index"] == float64(0) {
				foundAssistantAdded = true
			}
			if item["type"] == "function_call" && item["call_id"] == "call_123" && event.Payload["output_index"] == float64(1) {
				foundToolAddedAtIndexOne = true
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "message" && item["role"] == "assistant" && event.Payload["output_index"] == float64(0) {
				foundAssistantDone = true
			}
		case "response.output_text.delta":
			if event.Payload["delta"] == "I'll check that for you." {
				foundTextDelta = true
			}
		}
	}

	if !foundTextDelta {
		t.Fatal("expected response.output_text.delta for assistant preamble")
	}
	if !foundAssistantAdded {
		t.Fatal("expected assistant message response.output_item.added at output_index 0")
	}
	if !foundAssistantDone {
		t.Fatal("expected assistant message response.output_item.done at output_index 0")
	}
	if !foundToolAddedAtIndexOne {
		t.Fatal("expected function_call output_index to be 1 after assistant text")
	}
}

func TestOpenAIResponsesStreamConverter_WaitsForToolMetadata(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := NewOpenAIResponsesStreamConverter(reader, "test-model", "groq")

	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	addedCount := 0
	var argumentDeltas []string

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				addedCount++
				if item["call_id"] != "call_123" {
					t.Fatalf("function_call call_id = %v, want call_123", item["call_id"])
				}
				if item["name"] != "lookup_weather" {
					t.Fatalf("function_call name = %v, want lookup_weather", item["name"])
				}
			}
		case "response.function_call_arguments.delta":
			if delta, _ := event.Payload["delta"].(string); delta != "" {
				argumentDeltas = append(argumentDeltas, delta)
			}
		}
	}

	if addedCount != 1 {
		t.Fatalf("function_call added event count = %d, want 1", addedCount)
	}
	if len(argumentDeltas) != 1 || argumentDeltas[0] != `{"city":"Warsaw"}` {
		t.Fatalf("response.function_call_arguments.delta = %#v, want buffered JSON after metadata", argumentDeltas)
	}
}

func parseTestSSEEvents(t *testing.T, raw string) []testSSEEvent {
	t.Helper()

	lines := strings.Split(raw, "\n")
	events := make([]testSSEEvent, 0)
	currentEventName := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			currentEventName = strings.TrimSpace(after)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			events = append(events, testSSEEvent{Name: currentEventName, Done: true})
			currentEventName = ""
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("failed to unmarshal SSE payload %q: %v", data, err)
		}

		events = append(events, testSSEEvent{
			Name:    currentEventName,
			Payload: payload,
		})
		currentEventName = ""
	}

	return events
}

// TestOpenAIResponsesStreamConverter_TolerantChunkFallback covers chunks that
// fail the typed fast-path decode: one off-spec member must only skip itself,
// not discard the chunk's remaining deltas or usage.
func TestOpenAIResponsesStreamConverter_TolerantChunkFallback(t *testing.T) {
	// content is an off-spec parts array; usage and finish_reason must survive.
	// The second chunk carries a float tool-call index (Python-style encoders)
	// alongside junk entries (non-object, index missing) that must be skipped
	// without discarding the valid call.
	mockStream := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"ignored"}]},"finish_reason":null}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}

data: {"choices":[{"delta":{"tool_calls":["junk",{"id":"call_no_index"},{"index":0.0,"id":"call_f","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "groq")
	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	var completed map[string]any
	foundToolAdded := false
	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.completed":
			completed, _ = event.Payload["response"].(map[string]any)
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" && item["name"] == "lookup" {
				foundToolAdded = true
			}
		}
	}

	if completed == nil {
		t.Fatal("expected response.completed event")
	}
	usage, ok := completed["usage"].(map[string]any)
	if !ok {
		t.Fatalf("response.completed usage = %#v, want object captured from off-spec chunk", completed["usage"])
	}
	if usage["total_tokens"] != float64(7) {
		t.Fatalf("usage total_tokens = %v, want 7", usage["total_tokens"])
	}
	if !foundToolAdded {
		t.Fatal("expected function_call output item from float-index tool call delta")
	}
}

// TestOpenAIResponsesStreamConverter_DropsNonObjectUsage ensures off-spec
// non-object usage values never leak into the response.completed payload.
func TestOpenAIResponsesStreamConverter_DropsNonObjectUsage(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}],"usage":"n/a"}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "groq")
	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ := event.Payload["response"].(map[string]any)
		if response == nil {
			t.Fatal("response.completed missing response object")
		}
		if usage, present := response["usage"]; present {
			t.Fatalf("response.completed usage = %#v, want omitted for non-object usage", usage)
		}
		return
	}
	t.Fatal("expected response.completed event")
}
