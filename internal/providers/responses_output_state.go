package providers

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/google/uuid"
)

// ResponsesOutputToolCallState tracks one function_call item in a Responses stream.
type ResponsesOutputToolCallState struct {
	ItemID            string
	CallID            string
	Name              string
	OutputIndex       int
	Arguments         strings.Builder
	Started           bool
	Completed         bool
	PlaceholderObject bool
}

// ResponsesOutputEventState manages assistant/tool output items for Responses streams.
type ResponsesOutputEventState struct {
	responseID         string
	assistantReserved  bool
	assistantStarted   bool
	assistantDone      bool
	assistantMessageID string
	assistantText      strings.Builder

	reasoningReserved bool
	reasoningStarted  bool
	reasoningDone     bool
	reasoningItemID   string
	reasoningText     strings.Builder
}

// NewResponsesOutputEventState creates a new Responses output-item state manager.
func NewResponsesOutputEventState(responseID string) *ResponsesOutputEventState {
	return &ResponsesOutputEventState{responseID: responseID}
}

// WriteEvent renders one SSE event in Responses API format.
func (s *ResponsesOutputEventState) WriteEvent(eventName string, payload map[string]any) string {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal responses stream event", "error", err, "event", eventName, "response_id", s.responseID)
		return ""
	}
	var b strings.Builder
	b.Grow(len("event: \ndata: \n\n") + len(eventName) + len(jsonData))
	b.WriteString("event: ")
	b.WriteString(eventName)
	b.WriteString("\ndata: ")
	b.Write(jsonData)
	b.WriteString("\n\n")
	return b.String()
}

// ReserveAssistant marks that the assistant message output item occupies index 0.
func (s *ResponsesOutputEventState) ReserveAssistant() {
	s.assistantReserved = true
}

// AssistantReserved reports whether the assistant output item has been reserved.
func (s *ResponsesOutputEventState) AssistantReserved() bool {
	return s.assistantReserved
}

// AssistantStarted reports whether the assistant output item has been emitted.
func (s *ResponsesOutputEventState) AssistantStarted() bool {
	return s.assistantStarted
}

// AssistantDone reports whether the assistant output item has been completed.
func (s *ResponsesOutputEventState) AssistantDone() bool {
	return s.assistantDone
}

// AppendAssistantText appends assistant text content to the output item buffer.
func (s *ResponsesOutputEventState) AppendAssistantText(text string) {
	_, _ = s.assistantText.WriteString(text)
}

// AssistantMessageItem renders the assistant message output item payload.
func (s *ResponsesOutputEventState) AssistantMessageItem(status string, includeContent bool) map[string]any {
	item := map[string]any{
		"id":      s.assistantMessageID,
		"type":    "message",
		"status":  status,
		"role":    "assistant",
		"content": []map[string]any{},
	}
	if includeContent {
		item["content"] = []map[string]any{
			{
				"type":        "output_text",
				"text":        s.assistantText.String(),
				"annotations": []json.RawMessage{},
			},
		}
	}
	return item
}

// StartAssistantOutput emits the assistant message output_item.added event once.
func (s *ResponsesOutputEventState) StartAssistantOutput(outputIndex int) string {
	if s.assistantStarted {
		return ""
	}
	s.assistantStarted = true
	if s.assistantMessageID == "" {
		s.assistantMessageID = "msg_" + uuid.New().String()
	}
	return s.WriteEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"item":         s.AssistantMessageItem("in_progress", false),
		"output_index": outputIndex,
	})
}

// CompleteAssistantOutput emits the assistant message output_item.done event once.
func (s *ResponsesOutputEventState) CompleteAssistantOutput(outputIndex int) string {
	if !s.assistantReserved || s.assistantDone {
		return ""
	}
	s.assistantDone = true
	return s.StartAssistantOutput(outputIndex) + s.WriteEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"item":         s.AssistantMessageItem("completed", true),
		"output_index": outputIndex,
	})
}

// ReserveReasoning marks that a reasoning output item occupies index 0,
// ahead of the assistant message and any tool calls.
func (s *ResponsesOutputEventState) ReserveReasoning() {
	s.reasoningReserved = true
}

// ReasoningReserved reports whether a reasoning output item has been reserved.
func (s *ResponsesOutputEventState) ReasoningReserved() bool {
	return s.reasoningReserved
}

// ReasoningDone reports whether the reasoning output item has been completed.
func (s *ResponsesOutputEventState) ReasoningDone() bool {
	return s.reasoningDone
}

// ReasoningItem renders a raw reasoning output item. Provider
// reasoning_content is chain-of-thought text, not an OpenAI-generated summary,
// so it belongs in content while summary remains empty.
func (s *ResponsesOutputEventState) ReasoningItem(status string, includeContent bool) map[string]any {
	item := map[string]any{
		"id":      s.reasoningItemID,
		"type":    "reasoning",
		"status":  status,
		"summary": []map[string]any{},
	}
	if includeContent {
		item["content"] = []map[string]any{
			{"type": "reasoning_text", "text": s.reasoningText.String()},
		}
	}
	return item
}

// StartReasoningOutput emits the reasoning output_item.added event once before
// any reasoning_text delta references the item.
func (s *ResponsesOutputEventState) StartReasoningOutput(outputIndex int) string {
	if s.reasoningStarted {
		return ""
	}
	s.reasoningStarted = true
	if s.reasoningItemID == "" {
		s.reasoningItemID = "rs_" + uuid.New().String()
	}
	return s.WriteEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"item":         s.ReasoningItem("in_progress", false),
		"output_index": outputIndex,
	})
}

// AppendReasoningDelta starts the reasoning item if needed and emits a raw
// reasoning-text delta.
func (s *ResponsesOutputEventState) AppendReasoningDelta(outputIndex int, delta string) string {
	var b strings.Builder
	b.WriteString(s.StartReasoningOutput(outputIndex))
	s.reasoningText.WriteString(delta)
	b.WriteString(s.WriteEvent("response.reasoning_text.delta", map[string]any{
		"type":          "response.reasoning_text.delta",
		"item_id":       s.reasoningItemID,
		"output_index":  outputIndex,
		"content_index": 0,
		"delta":         delta,
	}))
	return b.String()
}

// CompleteReasoningOutput emits the raw reasoning completion and
// output_item.done events once.
func (s *ResponsesOutputEventState) CompleteReasoningOutput(outputIndex int) string {
	if !s.reasoningReserved || s.reasoningDone {
		return ""
	}
	s.reasoningDone = true
	var b strings.Builder
	b.WriteString(s.StartReasoningOutput(outputIndex))
	b.WriteString(s.WriteEvent("response.reasoning_text.done", map[string]any{
		"type":          "response.reasoning_text.done",
		"item_id":       s.reasoningItemID,
		"output_index":  outputIndex,
		"content_index": 0,
		"text":          s.reasoningText.String(),
	}))
	b.WriteString(s.WriteEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"item":         s.ReasoningItem("completed", true),
		"output_index": outputIndex,
	}))
	return b.String()
}

// FinalOutputItems assembles the full output array for the terminal
// response.completed event, mirroring the output_item.done payloads already
// emitted on the stream, ordered by output index. OpenAI includes the complete
// output in response.completed, and strict SDK clients index into it. Only
// tool calls whose output_item.done has been emitted are included — callers
// must finalize pending tool calls first.
func (s *ResponsesOutputEventState) FinalOutputItems(reasoningIndex, assistantIndex int, toolCalls map[int]*ResponsesOutputToolCallState, includePlaceholder bool) []map[string]any {
	type indexedItem struct {
		index int
		item  map[string]any
	}
	items := make([]indexedItem, 0, 2+len(toolCalls))
	if s.reasoningStarted {
		items = append(items, indexedItem{reasoningIndex, s.ReasoningItem("completed", true)})
	}
	if s.assistantStarted {
		items = append(items, indexedItem{assistantIndex, s.AssistantMessageItem("completed", true)})
	}
	for _, state := range toolCalls {
		if state == nil || !state.Started || !state.Completed {
			continue
		}
		items = append(items, indexedItem{state.OutputIndex, s.RenderToolCallItem(state, "completed", includePlaceholder)})
	}
	slices.SortStableFunc(items, func(a, b indexedItem) int { return a.index - b.index })
	output := make([]map[string]any, 0, len(items))
	for _, entry := range items {
		output = append(output, entry.item)
	}
	return output
}

// ToolCallArguments returns the serialized argument payload for a function_call item.
func (s *ResponsesOutputEventState) ToolCallArguments(state *ResponsesOutputToolCallState) string {
	if state == nil {
		return ""
	}
	if state.PlaceholderObject && state.Arguments.Len() == 0 {
		return "{}"
	}
	return state.Arguments.String()
}

// RenderToolCallItem renders a function_call output item payload.
func (s *ResponsesOutputEventState) RenderToolCallItem(state *ResponsesOutputToolCallState, status string, includePlaceholder bool) map[string]any {
	arguments := state.Arguments.String()
	if includePlaceholder {
		arguments = s.ToolCallArguments(state)
	}
	return map[string]any{
		"id":        state.ItemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   state.CallID,
		"name":      state.Name,
		"arguments": arguments,
	}
}

// StartToolCall emits the function_call output_item.added event once the item metadata is available.
func (s *ResponsesOutputEventState) StartToolCall(state *ResponsesOutputToolCallState, includePlaceholder bool) string {
	if state == nil || state.Started {
		return ""
	}
	if strings.TrimSpace(state.CallID) == "" || strings.TrimSpace(state.Name) == "" {
		return ""
	}
	state.CallID = ResponsesFunctionCallCallID(state.CallID)
	state.ItemID = ResponsesFunctionCallItemID(state.CallID)
	state.Started = true
	return s.WriteEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"item":         s.RenderToolCallItem(state, "in_progress", includePlaceholder),
		"output_index": state.OutputIndex,
	})
}

// CompleteToolCall emits the argument completion and output_item.done events once.
func (s *ResponsesOutputEventState) CompleteToolCall(state *ResponsesOutputToolCallState, includePlaceholder bool) string {
	if state == nil || state.Completed {
		return ""
	}
	state.Completed = true
	return s.WriteEvent("response.function_call_arguments.done", map[string]any{
		"type":         "response.function_call_arguments.done",
		"item_id":      state.ItemID,
		"output_index": state.OutputIndex,
		"arguments":    s.ToolCallArguments(state),
	}) + s.WriteEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"item":         s.RenderToolCallItem(state, "completed", includePlaceholder),
		"output_index": state.OutputIndex,
	})
}
