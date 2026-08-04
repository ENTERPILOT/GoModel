package providers

import (
	"bytes"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// OpenAIResponsesStreamConverter wraps an OpenAI-compatible SSE stream
// and converts it to Responses API format.
// Used by providers that have OpenAI-compatible streaming (Groq, Gemini, etc.)
type OpenAIResponsesStreamConverter struct {
	reader               io.ReadCloser
	model                string
	provider             string
	responseID           string
	createdAt            int64
	output               *ResponsesOutputEventState
	toolCalls            map[int]*ResponsesOutputToolCallState
	assistantOutputIndex int
	nextOutputIndex      int
	buffer               streaming.StreamBuffer
	lineBuffer           streaming.StreamBuffer
	readBuf              []byte
	closed               bool
	sentCreate           bool
	sentDone             bool
	cachedUsage          json.RawMessage // Stores usage from final chunk for inclusion in response.completed
}

// NewOpenAIResponsesStreamConverter creates a new converter that transforms
// OpenAI-format SSE streams to Responses API format.
func NewOpenAIResponsesStreamConverter(reader io.ReadCloser, model, provider string) *OpenAIResponsesStreamConverter {
	responseID := "resp_" + uuid.New().String()
	return &OpenAIResponsesStreamConverter{
		reader:     reader,
		model:      model,
		provider:   provider,
		responseID: responseID,
		createdAt:  time.Now().Unix(),
		output:     NewResponsesOutputEventState(responseID),
		toolCalls:  make(map[int]*ResponsesOutputToolCallState),
		buffer:     streaming.NewStreamBuffer(4096),
		lineBuffer: streaming.NewStreamBuffer(1024),
		readBuf:    make([]byte, 1024),
	}
}

// openAIStreamChunk is the subset of an OpenAI chat.completion.chunk the
// converter consumes. Typed decoding avoids a map[string]any per chunk.
type openAIStreamChunk struct {
	Error   json.RawMessage `json:"error"`
	Usage   json.RawMessage `json:"usage"`
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openAIChunkToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// responsesStreamUsage is the conservative Responses API representation of a
// Chat Completions usage object. Keeping this local avoids the generic unknown-
// field preservation path on every streamed response.
type responsesStreamUsage struct {
	InputTokens         int                           `json:"input_tokens"`
	OutputTokens        int                           `json:"output_tokens"`
	TotalTokens         int                           `json:"total_tokens"`
	InputTokensDetails  *core.PromptTokensDetails     `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *core.CompletionTokensDetails `json:"output_tokens_details,omitempty"`
}

type openAIChunkToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (sc *OpenAIResponsesStreamConverter) ensureToolCallState(index int) *ResponsesOutputToolCallState {
	state := sc.toolCalls[index]
	if state == nil {
		state = &ResponsesOutputToolCallState{OutputIndex: -1}
		sc.toolCalls[index] = state
	}
	return state
}

// reasoningOutputIndex is always 0: reasoning content precedes any visible
// text or tool call in a reasoning model's stream, so the reasoning item (if
// any) always claims the first output slot.
const reasoningOutputIndex = 0

func (sc *OpenAIResponsesStreamConverter) reserveReasoningOutput() {
	if sc.output.ReasoningReserved() {
		return
	}
	sc.output.ReserveReasoning()
	sc.nextOutputIndex++
}

func (sc *OpenAIResponsesStreamConverter) outputAlreadyStarted() bool {
	if sc.output.AssistantStarted() {
		return true
	}
	for _, state := range sc.toolCalls {
		if state != nil && state.Started {
			return true
		}
	}
	return false
}

func (sc *OpenAIResponsesStreamConverter) reserveAssistantOutput() {
	if sc.output.AssistantReserved() {
		return
	}

	sc.assistantOutputIndex = sc.nextOutputIndex
	sc.nextOutputIndex++
	sc.output.ReserveAssistant()
}

func (sc *OpenAIResponsesStreamConverter) startToolCall(state *ResponsesOutputToolCallState) string {
	if state == nil || state.Started || strings.TrimSpace(state.CallID) == "" || strings.TrimSpace(state.Name) == "" {
		return ""
	}
	if state.OutputIndex < 0 {
		state.OutputIndex = sc.nextOutputIndex
		sc.nextOutputIndex++
	}
	return sc.output.StartToolCall(state, false)
}

func (sc *OpenAIResponsesStreamConverter) forceStartToolCall(state *ResponsesOutputToolCallState) string {
	if state.Started {
		return ""
	}
	if strings.TrimSpace(state.Name) == "" {
		state.Name = "unknown"
	}
	return sc.startToolCall(state)
}

func (sc *OpenAIResponsesStreamConverter) completePendingToolCalls() string {
	indices := make([]int, 0, len(sc.toolCalls))
	for index := range sc.toolCalls {
		indices = append(indices, index)
	}
	slices.Sort(indices)

	var out bytes.Buffer
	for _, index := range indices {
		state := sc.toolCalls[index]
		if state == nil || state.Completed {
			continue
		}
		out.WriteString(sc.forceStartToolCall(state))
		if !state.Started {
			continue
		}
		out.WriteString(sc.output.CompleteToolCall(state, false))
	}

	return out.String()
}

func (sc *OpenAIResponsesStreamConverter) handleToolCallDeltas(toolCalls []openAIChunkToolCall) string {
	var out bytes.Buffer

	out.WriteString(sc.output.CompleteReasoningOutput(reasoningOutputIndex))
	if sc.output.AssistantStarted() && !sc.output.AssistantDone() {
		out.WriteString(sc.output.CompleteAssistantOutput(sc.assistantOutputIndex))
	}

	for _, toolCall := range toolCalls {
		if toolCall.Index == nil {
			continue
		}

		state := sc.ensureToolCallState(*toolCall.Index)
		if toolCall.ID != "" {
			state.CallID = toolCall.ID
		}
		if toolCall.Function.Name != "" {
			state.Name = toolCall.Function.Name
		}

		arguments := toolCall.Function.Arguments
		hadStarted := state.Started
		if arguments != "" {
			_, _ = state.Arguments.WriteString(arguments)
		}
		out.WriteString(sc.startToolCall(state))

		if state.Started {
			delta := ""
			if !hadStarted && state.Arguments.Len() > 0 {
				delta = state.Arguments.String()
			} else if arguments != "" {
				delta = arguments
			}
			if delta != "" {
				out.WriteString(sc.output.WriteEvent("response.function_call_arguments.delta", map[string]any{
					"type":         "response.function_call_arguments.delta",
					"item_id":      state.ItemID,
					"output_index": state.OutputIndex,
					"delta":        delta,
				}))
			}
		}
	}

	return out.String()
}

// processChunk translates one chat.completion.chunk payload into Responses
// API events appended to the output buffer.
func (sc *OpenAIResponsesStreamConverter) processChunk(data []byte) {
	var chunk openAIStreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		// One off-spec member type aborts the whole typed decode; re-parse
		// tolerantly so the chunk's remaining deltas and usage still flow
		// (Postel's law), paying the generic-map cost only for such chunks.
		sc.processChunkTolerant(data)
		return
	}
	if len(bytes.TrimSpace(chunk.Error)) > 0 && !bytes.Equal(bytes.TrimSpace(chunk.Error), []byte("null")) {
		sc.appendFailedEvents(chunk.Error)
		return
	}

	// Capture usage if present and object-shaped (OpenAI sends it in the
	// final chunk); anything else must not leak into response.completed.
	if usage := bytes.TrimSpace(chunk.Usage); len(usage) > 0 && usage[0] == '{' {
		sc.cachedUsage = usage
	}

	if len(chunk.Choices) == 0 {
		return
	}
	choice := &chunk.Choices[0]

	if choice.Delta.ReasoningContent != "" {
		sc.appendReasoningDelta(choice.Delta.ReasoningContent)
	}
	if choice.Delta.Content != "" {
		sc.appendTextDelta(choice.Delta.Content)
	}
	if len(choice.Delta.ToolCalls) > 0 {
		sc.buffer.AppendString(sc.handleToolCallDeltas(choice.Delta.ToolCalls))
	}
	if choice.FinishReason == "tool_calls" {
		sc.buffer.AppendString(sc.completePendingToolCalls())
	}
}

// processChunkTolerant mirrors processChunk with per-field type assertions, so
// a single off-spec member only skips itself instead of the whole chunk.
func (sc *OpenAIResponsesStreamConverter) processChunkTolerant(data []byte) {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return
	}
	if streamErr, present := chunk["error"]; present && streamErr != nil {
		raw, err := json.Marshal(streamErr)
		if err == nil {
			sc.appendFailedEvents(raw)
		}
		return
	}

	if usage, ok := chunk["usage"].(map[string]any); ok {
		if raw, err := json.Marshal(usage); err == nil {
			sc.cachedUsage = raw
		}
	}

	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) == 0 {
		return
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return
	}
	if delta, ok := choice["delta"].(map[string]any); ok {
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			sc.appendReasoningDelta(reasoning)
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			sc.appendTextDelta(content)
		}
		if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
			sc.buffer.AppendString(sc.handleToolCallDeltas(chunkToolCallsFromAny(toolCalls)))
		}
	}
	if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
		sc.buffer.AppendString(sc.completePendingToolCalls())
	}
}

// chunkToolCallsFromAny converts generically parsed tool-call deltas into the
// typed form, dropping entries without a usable numeric index.
func chunkToolCallsFromAny(items []any) []openAIChunkToolCall {
	calls := make([]openAIChunkToolCall, 0, len(items))
	for _, item := range items {
		toolCall, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index, ok := normalizeToolCallIndex(toolCall["index"])
		if !ok {
			continue
		}
		call := openAIChunkToolCall{Index: &index}
		call.ID, _ = toolCall["id"].(string)
		if function, ok := toolCall["function"].(map[string]any); ok {
			call.Function.Name, _ = function["name"].(string)
			call.Function.Arguments, _ = function["arguments"].(string)
		}
		calls = append(calls, call)
	}
	return calls
}

func normalizeToolCallIndex(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// appendReasoningDelta records raw provider reasoning and starts the reasoning
// item before emitting its reasoning_text delta.
func (sc *OpenAIResponsesStreamConverter) appendReasoningDelta(content string) {
	// Output indexes cannot be rewritten after an item has been emitted. Some
	// OpenAI-compatible providers send a stray late reasoning delta; dropping
	// that extension is safer than producing two items at index 0 or reopening
	// an item after response.output_item.done.
	if sc.output.ReasoningDone() || (!sc.output.ReasoningReserved() && sc.outputAlreadyStarted()) {
		return
	}
	sc.reserveReasoningOutput()
	sc.buffer.AppendString(sc.output.AppendReasoningDelta(reasoningOutputIndex, content))
}

// appendTextDelta records assistant text and emits its output_text.delta event.
func (sc *OpenAIResponsesStreamConverter) appendTextDelta(content string) {
	sc.buffer.AppendString(sc.output.CompleteReasoningOutput(reasoningOutputIndex))
	sc.reserveAssistantOutput()
	sc.buffer.AppendString(sc.output.StartAssistantOutput(sc.assistantOutputIndex))
	sc.output.AppendAssistantText(content)
	jsonData, err := json.Marshal(struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}{Type: "response.output_text.delta", Delta: content})
	if err != nil {
		slog.Error("failed to marshal content delta event", "error", err, "response_id", sc.responseID)
		return
	}
	sc.buffer.AppendString("event: response.output_text.delta\ndata: ")
	sc.buffer.AppendBytes(jsonData)
	sc.buffer.AppendString("\n\n")
}

// appendCompletedEvents flushes open output items and appends the final
// response.completed event plus the trailing [DONE] marker exactly once.
func (sc *OpenAIResponsesStreamConverter) appendCompletedEvents() {
	if sc.sentDone {
		return
	}
	sc.sentDone = true
	sc.buffer.AppendString(sc.output.CompleteReasoningOutput(reasoningOutputIndex))
	sc.buffer.AppendString(sc.output.CompleteAssistantOutput(sc.assistantOutputIndex))
	sc.buffer.AppendString(sc.completePendingToolCalls())
	responseData := map[string]any{
		"id":         sc.responseID,
		"object":     "response",
		"status":     "completed",
		"model":      sc.model,
		"provider":   sc.provider,
		"created_at": sc.createdAt,
	}
	// Include usage data if captured from OpenAI stream, renamed from Chat
	// Completions field names (prompt_tokens/completion_tokens) to the
	// Responses API's (input_tokens/output_tokens) — clients like Codex
	// require the latter and fail to parse response.completed without them.
	if sc.cachedUsage != nil {
		if usage, ok := chatUsageToResponsesUsage(sc.cachedUsage); ok {
			responseData["usage"] = usage
		}
	}
	doneEvent := map[string]any{
		"type":     "response.completed",
		"response": responseData,
	}
	jsonData, err := json.Marshal(doneEvent)
	if err != nil {
		slog.Error("failed to marshal response.completed event", "error", err, "response_id", sc.responseID)
		return
	}
	sc.buffer.AppendString("event: response.completed\ndata: ")
	sc.buffer.AppendBytes(jsonData)
	sc.buffer.AppendString("\n\ndata: [DONE]\n\n")
}

func (sc *OpenAIResponsesStreamConverter) appendFailedEvents(raw json.RawMessage) {
	if sc.sentDone {
		return
	}
	sc.sentDone = true

	var upstream struct {
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		_ = json.Unmarshal(raw, &upstream.Message)
	}
	var code string
	_ = json.Unmarshal(upstream.Code, &code)
	if code == "" {
		code = upstream.Type
	}
	if code == "" {
		code = "provider_error"
	}
	if strings.TrimSpace(upstream.Message) == "" {
		upstream.Message = "provider stream failed"
	}

	responseData := map[string]any{
		"id":         sc.responseID,
		"object":     "response",
		"status":     "failed",
		"model":      sc.model,
		"provider":   sc.provider,
		"created_at": sc.createdAt,
		"output":     []map[string]any{},
		"error": map[string]any{
			"code":    code,
			"message": upstream.Message,
		},
	}
	failedEvent := map[string]any{
		"type":     "response.failed",
		"response": responseData,
	}
	jsonData, err := json.Marshal(failedEvent)
	if err != nil {
		slog.Error("failed to marshal response.failed event", "error", err, "response_id", sc.responseID)
		return
	}
	sc.buffer.AppendString("event: response.failed\ndata: ")
	sc.buffer.AppendBytes(jsonData)
	sc.buffer.AppendString("\n\ndata: [DONE]\n\n")
}

// chatUsageToResponsesUsage renames a valid Chat Completions usage object into
// the conservative Responses API shape. Malformed usage is omitted rather than
// making clients reject an otherwise successful response.completed event.
func chatUsageToResponsesUsage(raw json.RawMessage) (responsesStreamUsage, bool) {
	var chatUsage struct {
		PromptTokens            int                           `json:"prompt_tokens"`
		CompletionTokens        int                           `json:"completion_tokens"`
		TotalTokens             int                           `json:"total_tokens"`
		PromptTokensDetails     *core.PromptTokensDetails     `json:"prompt_tokens_details"`
		CompletionTokensDetails *core.CompletionTokensDetails `json:"completion_tokens_details"`
	}
	chatUsage.PromptTokens = -1
	chatUsage.CompletionTokens = -1
	chatUsage.TotalTokens = -1
	if err := json.Unmarshal(raw, &chatUsage); err != nil {
		return responsesStreamUsage{}, false
	}
	if chatUsage.PromptTokens < 0 || chatUsage.CompletionTokens < 0 || chatUsage.TotalTokens < 0 {
		return responsesStreamUsage{}, false
	}
	return responsesStreamUsage{
		InputTokens:         chatUsage.PromptTokens,
		OutputTokens:        chatUsage.CompletionTokens,
		TotalTokens:         chatUsage.TotalTokens,
		InputTokensDetails:  chatUsage.PromptTokensDetails,
		OutputTokensDetails: chatUsage.CompletionTokensDetails,
	}, true
}

func (sc *OpenAIResponsesStreamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	// Send response.created event first
	if !sc.sentCreate {
		sc.sentCreate = true
		createdEvent := map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":         sc.responseID,
				"object":     "response",
				"status":     "in_progress",
				"model":      sc.model,
				"provider":   sc.provider,
				"created_at": sc.createdAt,
			},
		}
		jsonData, err := json.Marshal(createdEvent)
		if err != nil {
			slog.Error("failed to marshal response.created event", "error", err, "response_id", sc.responseID)
			return 0, nil
		}
		sc.buffer.AppendString("event: response.created\ndata: ")
		sc.buffer.AppendBytes(jsonData)
		sc.buffer.AppendString("\n\n")
		return sc.buffer.Read(p), nil
	}

	// Read from the underlying stream
	nr, readErr := sc.reader.Read(sc.readBuf)
	if nr > 0 {
		sc.lineBuffer.AppendBytes(sc.readBuf[:nr])

		// Process complete lines
		for {
			unread := sc.lineBuffer.Unread()
			idx := bytes.IndexByte(unread, '\n')
			if idx == -1 {
				break
			}

			line := unread[:idx]
			sc.lineBuffer.Consume(idx + 1)

			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			if data, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
				if bytes.Equal(data, []byte("[DONE]")) {
					sc.appendCompletedEvents()
					continue
				}
				sc.processChunk(data)
			}
		}
	}

	if readErr != nil {
		if readErr == io.EOF {
			// Send final done event if we haven't already
			sc.appendCompletedEvents()

			if sc.buffer.Len() > 0 {
				return sc.buffer.Read(p), nil
			}

			sc.closed = true
			sc.releaseBuffers()
			_ = sc.reader.Close()
			return 0, io.EOF
		}
		return 0, readErr
	}

	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	// No data yet, try again
	return 0, nil
}

func (sc *OpenAIResponsesStreamConverter) Close() error {
	if sc.closed {
		sc.releaseBuffers()
		return nil
	}
	sc.closed = true
	sc.releaseBuffers()
	return sc.reader.Close()
}

func (sc *OpenAIResponsesStreamConverter) releaseBuffers() {
	sc.buffer.Release()
	sc.lineBuffer.Release()
}
