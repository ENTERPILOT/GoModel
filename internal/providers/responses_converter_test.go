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

// TestOpenAIResponsesStreamConverter_ReasoningContent covers DeepSeek-style
// reasoning_content deltas. reasoning_content is raw reasoning, so it must be
// exposed as reasoning_text rather than mislabeled as a readable summary. The
// item must be registered before its first delta and the assistant message must
// shift to output_index 1.
func TestOpenAIResponsesStreamConverter_ReasoningContent(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning_content":"Think"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning_content":"ing..."},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := NewOpenAIResponsesStreamConverter(reader, "deepseek-v4-pro", "deepseek")

	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}
	rawStr := string(raw)

	events := parseTestSSEEvents(t, rawStr)
	activeItems := map[string]bool{}
	var reasoningItemID string
	var messageOutputIndex float64 = -1
	var reasoningDeltas strings.Builder
	sawReasoningDone := false
	sawSummaryEvent := false

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			id, _ := item["id"].(string)
			activeItems[id] = true
			if item["type"] == "reasoning" {
				reasoningItemID = id
				summary, ok := item["summary"].([]any)
				if !ok || len(summary) != 0 {
					t.Fatalf("reasoning output_item.added must carry an empty summary array, got %#v", item["summary"])
				}
				if item["status"] != "in_progress" {
					t.Fatalf("reasoning output_item.added status = %#v, want in_progress", item["status"])
				}
				if idx, _ := event.Payload["output_index"].(float64); idx != 0 {
					t.Fatalf("reasoning output_index = %v, want 0", event.Payload["output_index"])
				}
			}
			if item["type"] == "message" {
				messageOutputIndex, _ = event.Payload["output_index"].(float64)
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			id, _ := item["id"].(string)
			if item["type"] == "reasoning" {
				content, _ := item["content"].([]any)
				if len(content) != 1 {
					t.Fatalf("completed reasoning content = %#v, want one reasoning_text part", item["content"])
				}
				part, _ := content[0].(map[string]any)
				if part["type"] != "reasoning_text" || part["text"] != "Thinking..." {
					t.Fatalf("completed reasoning part = %#v", part)
				}
			}
			delete(activeItems, id)
		case "response.reasoning_text.delta":
			itemID, _ := event.Payload["item_id"].(string)
			if !activeItems[itemID] {
				t.Fatalf("%s referenced item %q before its response.output_item.added", event.Name, itemID)
			}
			delta, _ := event.Payload["delta"].(string)
			reasoningDeltas.WriteString(delta)
		case "response.reasoning_text.done":
			itemID, _ := event.Payload["item_id"].(string)
			if !activeItems[itemID] {
				t.Fatalf("%s referenced item %q after it closed", event.Name, itemID)
			}
			if event.Payload["text"] != "Thinking..." {
				t.Fatalf("reasoning_text.done text = %#v", event.Payload["text"])
			}
			sawReasoningDone = true
		case "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta",
			"response.reasoning_summary_text.done", "response.reasoning_summary_part.done":
			sawSummaryEvent = true
		case "response.output_text.delta":
			// Assistant text must only stream after the reasoning item closed.
			if reasoningItemID != "" && activeItems[reasoningItemID] {
				t.Fatalf("response.output_text.delta arrived while the reasoning item was still open")
			}
		}
	}

	if reasoningItemID == "" {
		t.Fatal("expected a reasoning output_item.added event")
	}
	if messageOutputIndex != 1 {
		t.Fatalf("message output_index = %v, want 1 (after the reasoning item at index 0)", messageOutputIndex)
	}
	if reasoningDeltas.String() != "Thinking..." || !sawReasoningDone {
		t.Fatalf("reasoning stream = %q, done=%v", reasoningDeltas.String(), sawReasoningDone)
	}
	if sawSummaryEvent {
		t.Fatalf("raw reasoning_content must not emit reasoning summary events:\n%s", rawStr)
	}
	if len(activeItems) != 0 {
		t.Fatalf("expected every output item to close by end of stream, still open: %#v", activeItems)
	}
}

func TestOpenAIResponsesStreamConverter_DropsLateReasoningWithoutCorruptingIndexes(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"answer"},"finish_reason":null}]}

data: {"choices":[{"delta":{"reasoning_content":"late trace"},"finish_reason":"stop"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if strings.HasPrefix(event.Name, "response.reasoning_") {
			t.Fatalf("late reasoning produced %s:\n%s", event.Name, raw)
		}
		if event.Name != "response.output_item.added" && event.Name != "response.output_item.done" {
			continue
		}
		item, _ := event.Payload["item"].(map[string]any)
		if item["type"] == "message" && event.Payload["output_index"] != float64(0) {
			t.Fatalf("assistant %s output_index = %#v, want 0", event.Name, event.Payload["output_index"])
		}
	}
}

// TestOpenAIResponsesStreamConverter_ReasoningThenToolCall covers a
// reasoning-only turn (the model reasons, then calls a tool without any
// visible text): the reasoning item must close before the function_call
// item opens, and the tool call must shift to output_index 1.
func TestOpenAIResponsesStreamConverter_ReasoningThenToolCall(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning_content":"Need the weather."},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := NewOpenAIResponsesStreamConverter(reader, "deepseek-v4-pro", "deepseek")

	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	var reasoningDoneIndex, toolAddedIndex float64 = -1, -1
	reasoningDoneBeforeToolAdded := false
	sawReasoningDone := false

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "reasoning" {
				reasoningDoneIndex, _ = event.Payload["output_index"].(float64)
				sawReasoningDone = true
			}
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				toolAddedIndex, _ = event.Payload["output_index"].(float64)
				reasoningDoneBeforeToolAdded = sawReasoningDone
			}
		}
	}

	if reasoningDoneIndex != 0 {
		t.Fatalf("reasoning output_item.done index = %v, want 0", reasoningDoneIndex)
	}
	if toolAddedIndex != 1 {
		t.Fatalf("function_call output_index = %v, want 1 (after the reasoning item)", toolAddedIndex)
	}
	if !reasoningDoneBeforeToolAdded {
		t.Fatal("expected the reasoning item to close before the function_call item opened")
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
	mockStream := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"ignored"}]},"finish_reason":null}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}

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
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(4) {
		t.Fatalf("Responses usage counts = %#v", usage)
	}
	inputDetails, _ := usage["input_tokens_details"].(map[string]any)
	outputDetails, _ := usage["output_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"] != float64(2) || outputDetails["reasoning_tokens"] != float64(1) {
		t.Fatalf("Responses usage details = %#v", usage)
	}
	if _, present := usage["prompt_tokens"]; present {
		t.Fatalf("usage retained Chat field names: %#v", usage)
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

func TestOpenAIResponsesStreamConverter_DropsMalformedObjectUsage(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":"unknown","completion_tokens":1,"total_tokens":1}}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "groq")
	raw, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ := event.Payload["response"].(map[string]any)
		if _, present := response["usage"]; present {
			t.Fatalf("malformed usage leaked into response.completed: %#v", response["usage"])
		}
		return
	}
	t.Fatal("expected response.completed event")
}

func TestOpenAIResponsesStreamConverter_PropagatesStreamError(t *testing.T) {
	tests := []struct {
		name        string
		errorChunk  string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "object error",
			errorChunk:  `{"error":{"type":"provider_error","message":"upstream generation timed out","param":null,"code":null}}`,
			wantCode:    "provider_error",
			wantMessage: "upstream generation timed out",
		},
		{
			name:        "scalar error",
			errorChunk:  `{"error":"capacity exhausted"}`,
			wantCode:    "provider_error",
			wantMessage: "capacity exhausted",
		},
		{
			name:        "tolerant fallback",
			errorChunk:  `{"error":{"message":"malformed companion field","code":"upstream_error"},"choices":"invalid"}`,
			wantCode:    "upstream_error",
			wantMessage: "malformed companion field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStream := `data: {"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}

data: ` + tt.errorChunk + `

data: [DONE]
`

			converter := NewOpenAIResponsesStreamConverter(
				io.NopCloser(strings.NewReader(mockStream)),
				"test-model",
				"cohere",
			)
			raw, err := io.ReadAll(converter)
			if err != nil {
				t.Fatalf("failed to read from converter: %v", err)
			}

			var failed map[string]any
			for _, event := range parseTestSSEEvents(t, string(raw)) {
				switch event.Name {
				case "response.completed":
					t.Fatalf("stream emitted response.completed after provider error:\n%s", raw)
				case "response.failed":
					failed, _ = event.Payload["response"].(map[string]any)
				}
			}
			if failed == nil {
				t.Fatalf("stream missing response.failed event:\n%s", raw)
			}
			if failed["status"] != "failed" || failed["provider"] != "cohere" {
				t.Fatalf("response.failed response = %#v", failed)
			}
			responseErr, _ := failed["error"].(map[string]any)
			if responseErr["code"] != tt.wantCode ||
				responseErr["message"] != tt.wantMessage {
				t.Fatalf("response.failed error = %#v", responseErr)
			}
		})
	}
}
