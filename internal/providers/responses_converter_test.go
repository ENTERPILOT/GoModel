package providers

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

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

	require.True(t, foundAdded)
	require.Len(t, argumentDeltas, 2)
	require.Equal(t, "{\"city\":\"War", argumentDeltas[0])
	require.Equal(t, "saw\"}", argumentDeltas[1])
	require.True(t, foundArgumentsDone)
	require.True(t, foundItemDone)
}

// TestOpenAIResponsesStreamConverter_ToolCallExtraContent keeps a provider's
// extra_content (Gemini's thought signature) on the function_call item so a
// client that echoes the item restores it.
func TestOpenAIResponsesStreamConverter_ToolCallExtraContent(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	want := map[string]any{"google": map[string]any{"thought_signature": "sig-1"}}
	seen := 0
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.output_item.added" && event.Name != "response.output_item.done" {
			continue
		}
		item, _ := event.Payload["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		seen++
		got := item["extra_content"]
		require.Equal(t, want, got)
	}
	require.Equal(t, 2, seen)
}

// TestOpenAIResponsesStreamConverter_NullExtraContentDeltaKeepsSignature: a
// later argument delta carrying extra_content: null must not erase the
// signature the first delta set.
func TestOpenAIResponsesStreamConverter_NullExtraContentDeltaKeepsSignature(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{"},"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"},"extra_content":null}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	want := map[string]any{"google": map[string]any{"thought_signature": "sig-1"}}
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.output_item.done" {
			continue
		}
		item, _ := event.Payload["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		got := item["extra_content"]
		require.Equal(t, want, got)

		return
	}
	t.Fatal("expected a function_call output_item.done event")
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
	require.NoError(t, err)

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
				require.True(t, ok)
				require.Empty(t, summary)
				require.Equal(t, "in_progress", item["status"])
				idx, _ := event.Payload["output_index"].(float64)
				require.Equal(t, float64(0), idx, "reasoning output_index = %v, want 0", event.Payload["output_index"])
			}
			if item["type"] == "message" {
				messageOutputIndex, _ = event.Payload["output_index"].(float64)
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			id, _ := item["id"].(string)
			if item["type"] == "reasoning" {
				content, _ := item["content"].([]any)
				require.Len(t, content, 1)

				part, _ := content[0].(map[string]any)
				require.Equal(t, "reasoning_text", part["type"])
				require.Equal(t, "Thinking...", part["text"], "completed reasoning part = %#v", part)
			}
			delete(activeItems, id)
		case "response.reasoning_text.delta":
			itemID, _ := event.Payload["item_id"].(string)
			require.True(t, activeItems[itemID], "%s referenced item %q before its response.output_item.added", event.Name, itemID)

			delta, _ := event.Payload["delta"].(string)
			reasoningDeltas.WriteString(delta)
		case "response.reasoning_text.done":
			itemID, _ := event.Payload["item_id"].(string)
			require.True(t, activeItems[itemID], "%s referenced item %q after it closed", event.Name, itemID)
			require.Equal(t, "Thinking...", event.Payload["text"])

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

	require.NotEmpty(t, reasoningItemID)
	require.Equal(t, float64(1), messageOutputIndex)
	require.Equal(t, "Thinking...", reasoningDeltas.String())
	require.True(t, sawReasoningDone)
	require.False(t, sawSummaryEvent, "raw reasoning_content must not emit reasoning summary events:\n%s", rawStr)
	require.Empty(t, activeItems)
}

func TestOpenAIResponsesStreamConverter_DropsLateReasoningWithoutCorruptingIndexes(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"answer"},"finish_reason":null}]}

data: {"choices":[{"delta":{"reasoning_content":"late trace"},"finish_reason":"stop"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		require.False(t, strings.HasPrefix(event.Name, "response.reasoning_"), "late reasoning produced %s:\n%s", event.Name, raw)

		if event.Name != "response.output_item.added" && event.Name != "response.output_item.done" {
			continue
		}
		item, _ := event.Payload["item"].(map[string]any)
		if item["type"] == "message" && event.Payload["output_index"] != float64(0) {
			t.Fatalf("assistant %s output_index = %#v, want 0", event.Name, event.Payload["output_index"])
		}
	}
}

func TestOpenAIResponsesStreamConverter_ReasoningToolOutputOrder(t *testing.T) {
	tests := []struct {
		name         string
		mockStream   string
		wantIndexes  map[string]float64
		wantSequence []string
	}{
		{
			name: "reasoning then tool call",
			mockStream: `data: {"choices":[{"delta":{"reasoning_content":"Need the weather."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`,
			wantIndexes:  map[string]float64{"reasoning": 0, "function_call": 1},
			wantSequence: []string{"reasoning", "function_call"},
		},
		{
			name: "assistant after started tool call",
			mockStream: `data: {"choices":[{"delta":{"reasoning_content":"Need a tool."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Tool selected."},"finish_reason":"stop"}]}

data: [DONE]
`,
			wantIndexes:  map[string]float64{"reasoning": 0, "function_call": 1, "message": 2},
			wantSequence: []string{"reasoning", "function_call", "message"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(tt.mockStream)), "test-model", "mock")
			raw, err := io.ReadAll(converter)
			require.NoError(t, err)

			addedIndexes := make(map[string]float64)
			var addedSequence []string
			reasoningDone := false
			reasoningDoneBeforeTool := false
			for _, event := range parseTestSSEEvents(t, string(raw)) {
				item, _ := event.Payload["item"].(map[string]any)
				itemType, _ := item["type"].(string)
				switch event.Name {
				case "response.output_item.done":
					if itemType == "reasoning" {
						reasoningDone = event.Payload["output_index"] == float64(0)
					}
				case "response.output_item.added":
					addedIndexes[itemType], _ = event.Payload["output_index"].(float64)
					addedSequence = append(addedSequence, itemType)
					if itemType == "function_call" {
						reasoningDoneBeforeTool = reasoningDone
					}
				}
			}

			require.Len(t, addedIndexes, len(tt.wantIndexes), "want indexes %#v", tt.wantIndexes)

			for itemType, wantIndex := range tt.wantIndexes {
				require.Equal(t, wantIndex, addedIndexes[itemType], "%s output_index", itemType)
			}
			require.Equal(t, tt.wantSequence, addedSequence, "output item sequence")
			require.True(t, reasoningDoneBeforeTool)
		})
	}
}

// TestOpenAIResponsesStreamConverter_CompletedIncludesOutput verifies the
// terminal response.completed event carries the full output array (reasoning,
// message, function_call in stream order), matching OpenAI's native behavior —
// strict SDK clients index into response.output.
func TestOpenAIResponsesStreamConverter_CompletedIncludesOutput(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"reasoning_content":"Need the weather."},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Checking."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	var output []any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ := event.Payload["response"].(map[string]any)
		output, _ = response["output"].([]any)
	}

	require.Len(t, output, 3)

	reasoning, _ := output[0].(map[string]any)
	require.Equal(t, "reasoning", reasoning["type"])
	require.Equal(t, "completed", reasoning["status"], "output[0] = %#v, want completed reasoning item", reasoning)

	reasoningContent, _ := reasoning["content"].([]any)
	require.Len(t, reasoningContent, 1, "reasoning content = %#v, want one reasoning_text part", reasoning["content"])
	part, _ := reasoningContent[0].(map[string]any)
	require.Equal(t, "reasoning_text", part["type"])
	require.Equal(t, "Need the weather.", part["text"], "reasoning part = %#v, want reasoning_text %q", reasoningContent[0], "Need the weather.")

	message, _ := output[1].(map[string]any)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "assistant", message["role"])
	require.Equal(t, "completed", message["status"], "output[1] = %#v, want completed assistant message", message)

	messageContent, _ := message["content"].([]any)
	require.Len(t, messageContent, 1, "message content = %#v, want one output_text part", message["content"])
	part, _ = messageContent[0].(map[string]any)
	require.Equal(t, "output_text", part["type"])
	require.Equal(t, "Checking.", part["text"], "message part = %#v, want output_text %q", messageContent[0], "Checking.")

	toolCall, _ := output[2].(map[string]any)
	require.Equal(t, "function_call", toolCall["type"])
	require.Equal(t, "completed", toolCall["status"], "output[2] = %#v, want completed function_call", toolCall)
	require.Equal(t, "call_1", toolCall["call_id"])
	require.Equal(t, "lookup_weather", toolCall["name"])
	require.Equal(t, `{"city":"Warsaw"}`, toolCall["arguments"], "function_call = %#v, want call_1 lookup_weather with recorded arguments", toolCall)
}

// TestOpenAIResponsesStreamConverter_CompletedEmptyOutputIsArray verifies a
// stream with no output items still yields output: [] rather than omitting the
// field or emitting null.
func TestOpenAIResponsesStreamConverter_CompletedEmptyOutputIsArray(t *testing.T) {
	mockStream := "data: [DONE]\n"

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	found := false
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		found = true
		response, _ := event.Payload["response"].(map[string]any)
		output, ok := response["output"].([]any)
		require.True(t, ok, "response.completed output = %#v, want an array", response["output"])
		require.Empty(t, output)
	}
	require.True(t, found)
}

// TestOpenAIResponsesStreamConverter_TruncatedStreamEndsIncomplete covers an
// upstream stream that dies without a finish_reason or [DONE]. The converter
// must close open items with status "incomplete" and end the stream with
// response.incomplete instead of fabricating completion.
func TestOpenAIResponsesStreamConverter_TruncatedStreamEndsIncomplete(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"lo"},"finish_reason":null}]}
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	itemDoneStatus := ""
	foundCompleted := false
	var response map[string]any
	sawDone := false
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			require.NotNil(t, response)

			sawDone = true
			continue
		}
		switch event.Name {
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "message" {
				itemDoneStatus, _ = item["status"].(string)
			}
		case "response.completed":
			foundCompleted = true
		case "response.incomplete":
			response, _ = event.Payload["response"].(map[string]any)
		}
	}

	require.False(t, foundCompleted)
	require.NotNil(t, response)
	require.True(t, sawDone)
	require.Equal(t, "incomplete", itemDoneStatus)
	require.Equal(t, "incomplete", response["status"])

	details, _ := response["incomplete_details"].(map[string]any)
	require.Equal(t, "interrupted", details["reason"], "incomplete_details = %#v, want reason interrupted", response["incomplete_details"])

	output, _ := response["output"].([]any)
	require.Len(t, output, 1)

	message, _ := output[0].(map[string]any)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "incomplete", message["status"], "output[0] = %#v, want incomplete assistant message", message)

	messageContent, _ := message["content"].([]any)
	require.Len(t, messageContent, 1, "message content = %#v, want one output_text part", message["content"])
	part, _ := messageContent[0].(map[string]any)
	require.Equal(t, "Hello", part["text"])
}

// failingReadCloser returns its data on the first read and the configured
// error afterwards, mimicking an upstream body that dies mid-transfer.
type failingReadCloser struct {
	data []byte
	err  error
	read bool
}

func (r *failingReadCloser) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

func (r *failingReadCloser) Close() error { return nil }

// TestOpenAIResponsesStreamConverter_NonEOFReadErrorEndsIncomplete covers an
// upstream body that fails with a non-EOF error (io.ErrUnexpectedEOF from a
// chunked body cut mid-transfer, a connection reset). The client must still
// receive the response.incomplete terminal event and [DONE] before the error
// surfaces.
func TestOpenAIResponsesStreamConverter_NonEOFReadErrorEndsIncomplete(t *testing.T) {
	reader := &failingReadCloser{
		data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n"),
		err:  io.ErrUnexpectedEOF,
	}

	converter := NewOpenAIResponsesStreamConverter(reader, "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.Equal(t, io.ErrUnexpectedEOF, err)

	var response map[string]any
	sawDone := false
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			sawDone = true
			continue
		}
		if event.Name == "response.incomplete" {
			response, _ = event.Payload["response"].(map[string]any)
		}
	}

	require.NotNil(t, response)
	require.True(t, sawDone)
	require.Equal(t, "incomplete", response["status"])
}

// TestOpenAIResponsesStreamConverter_IgnoresDeltaAfterToolCallClosed covers a
// stray argument delta arriving after finish_reason "tool_calls" closed the
// call: it must not mutate the arguments the output_item.done event declared,
// so the terminal output array stays identical to the emitted done events.
func TestOpenAIResponsesStreamConverter_IgnoresDeltaAfterToolCallClosed(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"garbage"}}]},"finish_reason":null}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	doneArguments := ""
	var output []any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				doneArguments, _ = item["arguments"].(string)
			}
		case "response.completed":
			response, _ := event.Payload["response"].(map[string]any)
			output, _ = response["output"].([]any)
		}
	}

	require.Equal(t, `{"city":"Warsaw"}`, doneArguments)
	require.Len(t, output, 1)
	toolCall, _ := output[0].(map[string]any)
	require.Equal(t, `{"city":"Warsaw"}`, toolCall["arguments"])
}

// TestOpenAIResponsesStreamConverter_FinishReasonWithoutDoneCompletes covers
// providers that close the stream after the finish_reason chunk without a
// trailing [DONE] marker: the model finished, so the stream must still end
// with response.completed.
func TestOpenAIResponsesStreamConverter_FinishReasonWithoutDoneCompletes(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	var response map[string]any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ = event.Payload["response"].(map[string]any)
	}

	require.NotNil(t, response)
	require.Equal(t, "completed", response["status"])

	output, _ := response["output"].([]any)
	require.Len(t, output, 1)
	message, _ := output[0].(map[string]any)
	require.Equal(t, "completed", message["status"], "output[0] = %#v, want completed assistant message", message)
}

func TestOpenAIResponsesStreamConverter_OutOfOrderToolCallsKeepUniqueIndexes(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"reasoning_content":"Plan."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_second","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Calling tools."},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_first","type":"function","function":{"name":"first","arguments":"{}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`

	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	indexes := make(map[float64]string)
	callIndexes := make(map[string]float64)
	var addedIndexes []float64
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.output_item.added" {
			continue
		}
		index, ok := event.Payload["output_index"].(float64)
		require.True(t, ok, "output_index = %#v, want number", event.Payload["output_index"])

		item, _ := event.Payload["item"].(map[string]any)
		itemID, _ := item["id"].(string)
		previous, exists := indexes[index]
		require.False(t, exists, "output_index %v reused by %q and %q", index, previous, itemID)

		addedIndexes = append(addedIndexes, index)
		indexes[index] = itemID
		if callID, _ := item["call_id"].(string); callID != "" {
			callIndexes[callID] = index
		}
	}

	require.Len(t, indexes, 4)
	require.Equal(t, []float64{0, 1, 2, 3}, addedIndexes, "output indexes")
	require.Equal(t, float64(1), callIndexes["call_second"])
	require.Equal(t, float64(3), callIndexes["call_first"], "function-call indexes = %#v, want emission-order indexes 1 and 3", callIndexes)
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
	require.NoError(t, err)

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

	require.True(t, foundTextDelta)
	require.True(t, foundAssistantAdded)
	require.True(t, foundAssistantDone)
	require.True(t, foundToolAddedAtIndexOne)
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
	require.NoError(t, err)

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
				require.Equal(t, "call_123", item["call_id"])
				require.Equal(t, "lookup_weather", item["name"])
			}
		case "response.function_call_arguments.delta":
			if delta, _ := event.Payload["delta"].(string); delta != "" {
				argumentDeltas = append(argumentDeltas, delta)
			}
		}
	}

	require.Equal(t, 1, addedCount)
	require.Len(t, argumentDeltas, 1)
	require.Equal(t, `{"city":"Warsaw"}`, argumentDeltas[0])
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
		err := json.Unmarshal([]byte(data), &payload)
		require.NoError(t, err, "failed to unmarshal SSE payload %q: %v", data, err)

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
	require.NoError(t, err)

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

	require.NotNil(t, completed)

	usage, ok := completed["usage"].(map[string]any)
	require.True(t, ok, "response.completed usage = %#v, want object captured from off-spec chunk", completed["usage"])
	require.Equal(t, float64(7), usage["total_tokens"])
	require.Equal(t, float64(3), usage["input_tokens"])
	require.Equal(t, float64(4), usage["output_tokens"], "Responses usage counts = %#v", usage)

	inputDetails, _ := usage["input_tokens_details"].(map[string]any)
	outputDetails, _ := usage["output_tokens_details"].(map[string]any)
	require.Equal(t, float64(2), inputDetails["cached_tokens"])
	require.Equal(t, float64(1), outputDetails["reasoning_tokens"], "Responses usage details = %#v", usage)
	_, present := usage["prompt_tokens"]
	require.False(t, present, "usage retained Chat field names: %#v", usage)
	require.True(t, foundToolAdded)
}

func TestOpenAIResponsesStreamConverter_DropsInvalidUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage string
	}{
		{name: "non-object", usage: `"n/a"`},
		{name: "malformed required field", usage: `{"prompt_tokens":"unknown","completion_tokens":1,"total_tokens":1}`},
		{name: "required field nested", usage: `{"metadata":{"prompt_tokens":3},"completion_tokens":1,"total_tokens":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStream := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":` + tt.usage + `}

data: [DONE]
`
			converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "groq")
			raw, err := io.ReadAll(converter)
			require.NoError(t, err)

			for _, event := range parseTestSSEEvents(t, string(raw)) {
				if event.Done || event.Name != "response.completed" {
					continue
				}
				response, _ := event.Payload["response"].(map[string]any)
				require.NotNil(t, response)
				usage, present := response["usage"]
				require.False(t, present, "invalid usage leaked into response.completed: %#v", usage)

				return
			}
			t.Fatal("expected response.completed event")
		})
	}
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
			require.NoError(t, err)

			var failed map[string]any
			for _, event := range parseTestSSEEvents(t, string(raw)) {
				switch event.Name {
				case "response.completed":
					t.Fatalf("stream emitted response.completed after provider error:\n%s", raw)
				case "response.failed":
					failed, _ = event.Payload["response"].(map[string]any)
				}
			}
			require.NotNil(t, failed)
			require.Equal(t, "failed", failed["status"])
			require.Equal(t, "cohere", failed["provider"], "response.failed response = %#v", failed)

			responseErr, _ := failed["error"].(map[string]any)
			require.Equal(t, tt.wantCode, responseErr["code"])
			require.Equal(t, tt.wantMessage, responseErr["message"])
		})
	}
}

// A Gemini 3 text turn streams its thought signature on the last delta, after
// the text has started, as message-level extra_content. The reasoning slot is
// gone by then, so the signature rides on the assistant message item: its
// output_item.done and the terminal output, which is what the client echoes
// back. A later null delta must not clear it.
func TestOpenAIResponsesStreamConverter_MessageExtraContentOnMessageItem(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"content":"!","extra_content":{"google":{"thought_signature":"sig-text"}}},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"extra_content":null},"finish_reason":"stop"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "gemini-3.5-flash", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	want := map[string]any{"google": map[string]any{"thought_signature": "sig-text"}}
	sawDone := false
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			assert.NotEqual(t, "reasoning", item["type"])

		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] != "message" {
				continue
			}
			sawDone = true
			got := item["extra_content"]
			assert.Equal(t, want, got)

		case "response.completed":
			output, _ := event.Payload["response"].(map[string]any)["output"].([]any)
			require.Len(t, output, 1)
			got := output[0].(map[string]any)["extra_content"]
			assert.Equal(t, want, got)
		}
	}
	require.True(t, sawDone)
}

// The tolerant decode path, taken for off-spec chunks, must carry the member
// as well.
func TestOpenAIResponsesStreamConverter_MessageExtraContentTolerantPath(t *testing.T) {
	mockStream := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"ignored"}],"extra_content":{"google":{"thought_signature":"sig-text"}}},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Hi"},"finish_reason":"stop"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "gemini-3.5-flash", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	want := map[string]any{"google": map[string]any{"thought_signature": "sig-text"}}
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.completed" {
			continue
		}
		output, _ := event.Payload["response"].(map[string]any)["output"].([]any)
		require.Len(t, output, 1)
		got := output[0].(map[string]any)["extra_content"]
		require.Equal(t, want, got)

		return
	}
	t.Fatal("expected a response.completed event")
}

// A tool-call-only turn has no message item to carry a trailing message-level
// signature. Each call already carries its own, which is the one Gemini
// requires back, so the trailing one is dropped and the stream stays valid.
func TestOpenAIResponsesStreamConverter_MessageExtraContentWithoutMessage(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"extra_content":{"google":{"thought_signature":"sig-text"}}},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "gemini-3.5-flash", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.completed" {
			continue
		}
		output, _ := event.Payload["response"].(map[string]any)["output"].([]any)
		require.Len(t, output, 1)
		require.Equal(t, "function_call", output[0].(map[string]any)["type"])

		want := map[string]any{"google": map[string]any{"thought_signature": "sig-1"}}
		got := output[0].(map[string]any)["extra_content"]
		assert.Equal(t, want, got)

		return
	}
	t.Fatal("expected a response.completed event")
}

// One delta can carry text, a tool call, and the message-level signature at
// once. The tool call closes the message item immediately, so the signature
// must be recorded before that happens or the message's output_item.done
// disagrees with the terminal output.
func TestOpenAIResponsesStreamConverter_MessageExtraContentBeforeToolCallCloses(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"Checking.","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_weather","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}],"extra_content":{"google":{"thought_signature":"sig-text"}}},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gemini-3.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "gemini-3.5-flash", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	want := map[string]any{"google": map[string]any{"thought_signature": "sig-text"}}
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Name != "response.output_item.done" {
			continue
		}
		item, _ := event.Payload["item"].(map[string]any)
		if item["type"] != "message" {
			continue
		}
		got := item["extra_content"]
		require.Equal(t, want, got)

		return
	}
	t.Fatal("expected a message output_item.done event")
}

// eventNames lists the SSE event names in stream order, with "[DONE]" for the
// trailing marker.
func eventNames(events []testSSEEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		if event.Done {
			names = append(names, "[DONE]")
			continue
		}
		names = append(names, event.Name)
	}
	return names
}

// requireNormalizedResponsesStream checks the members OpenAI's Responses
// stream schema requires on every translated stream: a sequence_number that
// counts from zero across all events, response.created followed by
// response.in_progress, and the type member matching the SSE event name.
func requireNormalizedResponsesStream(t *testing.T, events []testSSEEvent) {
	t.Helper()
	require.GreaterOrEqual(t, len(events), 3, "events = %v, want at least created, in_progress and a terminal event", eventNames(events))
	require.Equal(t, "response.created", events[0].Name)
	require.Equal(t, "response.in_progress", events[1].Name, "stream opens with %v, want response.created then response.in_progress", eventNames(events)[:2])

	for i, name := range []string{"response.created", "response.in_progress"} {
		response, _ := events[i].Payload["response"].(map[string]any)
		require.Equal(t, "in_progress", response["status"], "%s response.status = %#v, want in_progress", name, response["status"])
		output, ok := // SDK stream helpers snapshot this object and append output items to it.
			response["output"].([]any)
		require.True(t, ok)
		require.Empty(t, output)
	}
	next := 0
	for _, event := range events {
		if event.Done {
			continue
		}
		require.Equal(t, event.Name, event.Payload["type"])

		seq, ok := event.Payload["sequence_number"].(float64)
		require.True(t, ok, "event %s has no sequence_number: %v", event.Name, event.Payload)
		require.Equal(t, next, int(seq), "event %s sequence_number", event.Name)

		next++
	}
}

// TestOpenAIResponsesStreamConverter_NormalizedTextStream pins the full
// event lifecycle of a streamed text message to the shape OpenAI emits, so a
// strict typed SDK sees the same stream whichever provider was routed.
func TestOpenAIResponsesStreamConverter_NormalizedTextStream(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	requireNormalizedResponsesStream(t, events)

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
		"[DONE]",
	}
	got := eventNames(events)
	require.Equal(t, want, got)

	item, _ := events[2].Payload["item"].(map[string]any)
	itemID, _ := item["id"].(string)
	require.NotEmpty(t, itemID, "output_item.added item has no id: %v", item)

	for _, event := range events[3:8] {
		require.Equal(t, itemID, event.Payload["item_id"], "%s item_id", event.Name)
		require.Equal(t, float64(0), event.Payload["output_index"])
		require.Equal(t, float64(0), event.Payload["content_index"], "%s indexes = %#v/%#v, want 0/0", event.Name, event.Payload["output_index"], event.Payload["content_index"])
	}
	partAdded, _ := events[3].Payload["part"].(map[string]any)
	require.Equal(t, "output_text", partAdded["type"])
	require.Empty(t, partAdded["text"], "content_part.added part = %#v, want empty output_text", partAdded)
	require.Equal(t, "Hello", events[4].Payload["delta"])
	require.Equal(t, " world", events[5].Payload["delta"])
	require.Equal(t, "Hello world", events[6].Payload["text"])

	partDone, _ := events[7].Payload["part"].(map[string]any)
	require.Equal(t, "output_text", partDone["type"])
	require.Equal(t, "Hello world", partDone["text"], "content_part.done part = %#v, want full output_text", partDone)
	_, ok := partDone["annotations"].([]any)
	require.True(t, ok, "content_part.done part has no annotations array: %#v", partDone)
}

// TestOpenAIResponsesStreamConverter_NormalizedToolCallStream keeps the
// stream schema members on a reasoning-plus-tool-call turn, which has no
// message item and therefore no content part.
func TestOpenAIResponsesStreamConverter_NormalizedToolCallStream(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"reasoning_content":"Think"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	requireNormalizedResponsesStream(t, events)

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.reasoning_text.delta",
		"response.reasoning_text.done",
		"response.output_item.done",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
		"[DONE]",
	}
	got := eventNames(events)
	require.Equal(t, want, got)
}

// TestOpenAIResponsesStreamConverter_InterruptedStreamClosesContentPart
// closes the open content part before the incomplete message item, so the
// partial text is restated the way OpenAI does on an interrupted stream.
func TestOpenAIResponsesStreamConverter_InterruptedStreamClosesContentPart(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	requireNormalizedResponsesStream(t, events)

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.incomplete",
		"[DONE]",
	}
	got := eventNames(events)
	require.Equal(t, want, got)
	require.Equal(t, "Hel", events[5].Payload["text"])

	item, _ := events[7].Payload["item"].(map[string]any)
	require.Equal(t, "incomplete", item["status"])
}

// TestOpenAIResponsesStreamConverter_FailedEventIsSequenced keeps the
// sequence_number on the response.failed terminal event.
func TestOpenAIResponsesStreamConverter_FailedEventIsSequenced(t *testing.T) {
	mockStream := `data: {"error":{"message":"upstream exploded","type":"server_error"}}

`
	converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "gemini")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	requireNormalizedResponsesStream(t, events)
	want := []string{"response.created", "response.in_progress", "response.failed", "[DONE]"}
	got := eventNames(events)
	require.Equal(t, want, got)
}
