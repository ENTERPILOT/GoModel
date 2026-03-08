package providers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

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
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventName, jsonData)
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
				"annotations": []string{},
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
