package providers

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
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
	FinalStatus       string // status carried by the item's output_item.done event
	PlaceholderObject bool
	// ExtraContent is the tool call's extra_content member (Gemini thought
	// signature), echoed on the function_call item.
	ExtraContent json.RawMessage
}

// ResponsesOutputEventState manages assistant/tool output items for Responses streams.
type ResponsesOutputEventState struct {
	responseID string
	// sequence is the sequence_number stamped on the next event. OpenAI
	// numbers every stream event from zero, and typed SDK parsers require it.
	sequence             int
	assistantReserved    bool
	assistantStarted     bool
	assistantPartStarted bool
	assistantDone        bool
	assistantMessageID   string
	assistantText        strings.Builder
	assistantFinalStatus string
	// assistantExtraContent is turn-wide provider replay state that arrived
	// on the message itself, such as a Gemini 3 text-turn thought signature.
	// Clients echo the message item back with it.
	assistantExtraContent json.RawMessage

	reasoningReserved    bool
	reasoningStarted     bool
	reasoningDone        bool
	reasoningItemID      string
	reasoningText        strings.Builder
	reasoningFinalStatus string
	// reasoningExtraContent is provider replay state for the reasoning item,
	// such as Anthropic thinking-block signatures. Clients echo it back.
	reasoningExtraContent json.RawMessage
}

// finalStatusOrCompleted defaults an unset item status to "completed" so items
// finished before status tracking existed keep their historical shape.
func finalStatusOrCompleted(status string) string {
	if status == "" {
		return "completed"
	}
	return status
}

// NewResponsesOutputEventState creates a new Responses output-item state manager.
func NewResponsesOutputEventState(responseID string) *ResponsesOutputEventState {
	return &ResponsesOutputEventState{responseID: responseID}
}

// WriteEvent renders one SSE event in Responses API format, stamping it with
// the next sequence_number.
func (s *ResponsesOutputEventState) WriteEvent(eventName string, payload map[string]any) string {
	payload["sequence_number"] = s.sequence
	return s.writeEncodedEvent(eventName, payload)
}

// writeEncodedEvent renders one SSE event whose payload already carries the
// current sequence_number, then advances the sequence.
func (s *ResponsesOutputEventState) writeEncodedEvent(eventName string, payload any) string {
	return s.writeEncodedEventWithTrailer(eventName, payload, "")
}

// writeEncodedEventWithTrailer is writeEncodedEvent with raw SSE bytes
// appended after the event, in the same allocation.
func (s *ResponsesOutputEventState) writeEncodedEventWithTrailer(eventName string, payload any, trailer string) string {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal responses stream event", "error", err, "event", eventName, "response_id", s.responseID)
		return ""
	}
	s.sequence++
	var b strings.Builder
	b.Grow(len("event: \ndata: \n\n") + len(eventName) + len(jsonData) + len(trailer))
	b.WriteString("event: ")
	b.WriteString(eventName)
	b.WriteString("\ndata: ")
	b.Write(jsonData)
	b.WriteString("\n\n")
	b.WriteString(trailer)
	return b.String()
}

// responsesLifecycleEvent is the typed payload of the events that carry the
// whole response object: response.created, response.in_progress and the
// terminal event.
type responsesLifecycleEvent struct {
	Type           string          `json:"type"`
	Response       json.RawMessage `json:"response"`
	SequenceNumber int             `json:"sequence_number"`
}

// writeLifecycleEvent emits one response-carrying event.
func (s *ResponsesOutputEventState) writeLifecycleEvent(eventName string, response json.RawMessage) string {
	return s.writeEncodedEvent(eventName, responsesLifecycleEvent{
		Type:           eventName,
		Response:       response,
		SequenceNumber: s.sequence,
	})
}

// encodeResponse renders a response object once for the lifecycle events
// that repeat it.
func (s *ResponsesOutputEventState) encodeResponse(eventName string, response map[string]any) (json.RawMessage, bool) {
	encoded, err := json.Marshal(response)
	if err != nil {
		slog.Error("failed to marshal responses stream response object", "error", err, "event", eventName, "response_id", s.responseID)
		return nil, false
	}
	return encoded, true
}

// StartResponse emits the response.created and response.in_progress events
// that open every OpenAI Responses stream, both carrying the given in-progress
// response object. The object gets an empty output array: SDK stream helpers
// snapshot it from response.created and append each output_item.added to it.
func (s *ResponsesOutputEventState) StartResponse(response map[string]any) string {
	response["output"] = []map[string]any{}
	encoded, ok := s.encodeResponse("response.created", response)
	if !ok {
		return ""
	}
	return s.writeLifecycleEvent("response.created", encoded) +
		s.writeLifecycleEvent("response.in_progress", encoded)
}

// FinishResponse emits the terminal event (response.completed, incomplete or
// failed) carrying the final response object, followed by the [DONE] marker.
// Open output items must be finished first so the terminal event sequences
// after their closing events.
func (s *ResponsesOutputEventState) FinishResponse(eventName string, response map[string]any) string {
	encoded, ok := s.encodeResponse(eventName, response)
	if !ok {
		return ""
	}
	return s.writeEncodedEventWithTrailer(eventName, responsesLifecycleEvent{
		Type:           eventName,
		Response:       encoded,
		SequenceNumber: s.sequence,
	}, "data: [DONE]\n\n")
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

// AppendAssistantDelta starts the assistant message item and its text
// content part if needed, records the text and emits its output_text.delta
// event addressed to that part.
func (s *ResponsesOutputEventState) AppendAssistantDelta(outputIndex int, delta string) string {
	var b strings.Builder
	b.WriteString(s.StartAssistantOutput(outputIndex))
	b.WriteString(s.startAssistantPart(outputIndex))
	_, _ = s.assistantText.WriteString(delta)
	b.WriteString(s.writeEncodedEvent("response.output_text.delta", responsesTextDeltaEvent{
		Type:           "response.output_text.delta",
		ItemID:         s.assistantMessageID,
		OutputIndex:    outputIndex,
		ContentIndex:   0,
		Delta:          delta,
		SequenceNumber: s.sequence,
	}))
	return b.String()
}

// responsesTextDeltaEvent is the typed output_text.delta payload: the
// per-token hot path of a text stream, kept off the generic map encoder.
type responsesTextDeltaEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
	SequenceNumber int    `json:"sequence_number"`
}

// responsesOutputTextPart is the message's single output_text content part.
type responsesOutputTextPart struct {
	Type        string            `json:"type"`
	Text        string            `json:"text"`
	Annotations []json.RawMessage `json:"annotations"`
}

// responsesContentPartEvent is the typed payload of content_part.added/done.
type responsesContentPartEvent struct {
	Type           string                  `json:"type"`
	ItemID         string                  `json:"item_id"`
	OutputIndex    int                     `json:"output_index"`
	ContentIndex   int                     `json:"content_index"`
	Part           responsesOutputTextPart `json:"part"`
	SequenceNumber int                     `json:"sequence_number"`
}

// responsesTextDoneEvent is the typed payload of output_text.done.
type responsesTextDoneEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Text           string `json:"text"`
	SequenceNumber int    `json:"sequence_number"`
}

// assistantPart renders the message's output_text content part.
func assistantPart(text string) map[string]any {
	return map[string]any{
		"type":        "output_text",
		"text":        text,
		"annotations": []json.RawMessage{},
	}
}

// writeAssistantPartEvent emits content_part.added or content_part.done
// carrying the part with the given text.
func (s *ResponsesOutputEventState) writeAssistantPartEvent(eventName string, outputIndex int, text string) string {
	return s.writeEncodedEvent(eventName, responsesContentPartEvent{
		Type:           eventName,
		ItemID:         s.assistantMessageID,
		OutputIndex:    outputIndex,
		ContentIndex:   0,
		Part:           responsesOutputTextPart{Type: "output_text", Text: text, Annotations: []json.RawMessage{}},
		SequenceNumber: s.sequence,
	})
}

// startAssistantPart emits response.content_part.added once, before the first
// text delta of the message.
func (s *ResponsesOutputEventState) startAssistantPart(outputIndex int) string {
	if s.assistantPartStarted {
		return ""
	}
	s.assistantPartStarted = true
	return s.writeAssistantPartEvent("response.content_part.added", outputIndex, "")
}

// finishAssistantPart emits response.output_text.done and
// response.content_part.done restating the message's full text.
func (s *ResponsesOutputEventState) finishAssistantPart(outputIndex int) string {
	text := s.assistantText.String()
	return s.writeEncodedEvent("response.output_text.done", responsesTextDoneEvent{
		Type:           "response.output_text.done",
		ItemID:         s.assistantMessageID,
		OutputIndex:    outputIndex,
		ContentIndex:   0,
		Text:           text,
		SequenceNumber: s.sequence,
	}) + s.writeAssistantPartEvent("response.content_part.done", outputIndex, text)
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
		item["content"] = []map[string]any{assistantPart(s.assistantText.String())}
	}
	if len(s.assistantExtraContent) > 0 {
		item[core.ExtraContentField] = s.assistantExtraContent
	}
	return item
}

// SetAssistantExtraContent attaches provider replay state to the assistant
// message item. It is rendered on every item written from here on, including
// the terminal output; a stream that carries it before the message starts
// still sees it on the item once the message exists.
func (s *ResponsesOutputEventState) SetAssistantExtraContent(raw json.RawMessage) {
	s.assistantExtraContent = raw
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
	return s.FinishAssistantOutput(outputIndex, "completed")
}

// FinishAssistantOutput closes the message's content part and emits the
// assistant message output_item.done event once, carrying the given terminal
// status ("completed", or "incomplete" when the upstream stream was
// interrupted before closing the message). The item always renders one
// output_text part, so the part's lifecycle events are emitted even when no
// text arrived.
func (s *ResponsesOutputEventState) FinishAssistantOutput(outputIndex int, status string) string {
	if !s.assistantReserved || s.assistantDone {
		return ""
	}
	s.assistantDone = true
	s.assistantFinalStatus = status
	return s.StartAssistantOutput(outputIndex) +
		s.startAssistantPart(outputIndex) +
		s.finishAssistantPart(outputIndex) +
		s.WriteEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"item":         s.AssistantMessageItem(status, true),
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
	if len(s.reasoningExtraContent) > 0 {
		item[core.ExtraContentField] = s.reasoningExtraContent
	}
	return item
}

// SetReasoningExtraContent attaches provider replay state to the reasoning
// item. It arrives after the item has been opened — an Anthropic signature
// only lands when the thinking block closes — so the value is rendered on
// every item written from here on, including the terminal output.
func (s *ResponsesOutputEventState) SetReasoningExtraContent(raw json.RawMessage) {
	s.reasoningExtraContent = raw
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
	return s.FinishReasoningOutput(outputIndex, "completed")
}

// FinishReasoningOutput emits the raw reasoning completion and output_item.done
// events once, carrying the given terminal status.
func (s *ResponsesOutputEventState) FinishReasoningOutput(outputIndex int, status string) string {
	if !s.reasoningReserved || s.reasoningDone {
		return ""
	}
	s.reasoningDone = true
	s.reasoningFinalStatus = status
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
		"item":         s.ReasoningItem(status, true),
		"output_index": outputIndex,
	}))
	return b.String()
}

// FinalOutputItems assembles the full output array for the terminal
// response.completed (or response.incomplete) event, mirroring the
// output_item.done payloads already emitted on the stream, ordered by output
// index. OpenAI includes the complete output in the terminal event, and strict
// SDK clients index into it. Each item carries the status its output_item.done
// event declared, and only tool calls whose output_item.done has been emitted
// are included — callers must finalize pending tool calls first.
func (s *ResponsesOutputEventState) FinalOutputItems(reasoningIndex, assistantIndex int, toolCalls map[int]*ResponsesOutputToolCallState, includePlaceholder bool) []map[string]any {
	type indexedItem struct {
		index int
		item  map[string]any
	}
	items := make([]indexedItem, 0, 2+len(toolCalls))
	if s.reasoningStarted {
		items = append(items, indexedItem{reasoningIndex, s.ReasoningItem(finalStatusOrCompleted(s.reasoningFinalStatus), true)})
	}
	if s.assistantStarted {
		items = append(items, indexedItem{assistantIndex, s.AssistantMessageItem(finalStatusOrCompleted(s.assistantFinalStatus), true)})
	}
	for _, state := range toolCalls {
		if state == nil || !state.Started || !state.Completed {
			continue
		}
		items = append(items, indexedItem{state.OutputIndex, s.RenderToolCallItem(state, finalStatusOrCompleted(state.FinalStatus), includePlaceholder)})
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
	item := map[string]any{
		"id":        state.ItemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   state.CallID,
		"name":      state.Name,
		"arguments": arguments,
	}
	if len(state.ExtraContent) > 0 {
		item["extra_content"] = state.ExtraContent
	}
	return item
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
	return s.FinishToolCall(state, "completed", includePlaceholder)
}

// FinishToolCall emits the argument completion and output_item.done events
// once, carrying the given terminal status.
func (s *ResponsesOutputEventState) FinishToolCall(state *ResponsesOutputToolCallState, status string, includePlaceholder bool) string {
	if state == nil || state.Completed {
		return ""
	}
	state.Completed = true
	state.FinalStatus = status
	return s.WriteEvent("response.function_call_arguments.done", map[string]any{
		"type":         "response.function_call_arguments.done",
		"item_id":      state.ItemID,
		"output_index": state.OutputIndex,
		"arguments":    s.ToolCallArguments(state),
	}) + s.WriteEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"item":         s.RenderToolCallItem(state, status, includePlaceholder),
		"output_index": state.OutputIndex,
	})
}
