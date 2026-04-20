package providers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/streaming"
)

// OpenAIResponsesStreamConverter wraps an OpenAI-compatible SSE stream
// and converts it to Responses API format.
// Used by providers that have OpenAI-compatible streaming (Groq, Gemini, etc.)
type OpenAIResponsesStreamConverter struct {
	reader      io.ReadCloser
	model       string
	provider    string
	responseID  string
	output      *ResponsesOutputEventState
	toolCalls   map[int]*ResponsesOutputToolCallState
	buffer      streaming.StreamBuffer
	lineBuffer  streaming.StreamBuffer
	closed      bool
	sentCreate  bool
	sentDone    bool
	cachedUsage map[string]any // Stores usage from final chunk for inclusion in response.completed
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
		output:     NewResponsesOutputEventState(responseID),
		toolCalls:  make(map[int]*ResponsesOutputToolCallState),
		buffer:     streaming.NewStreamBuffer(4096),
		lineBuffer: streaming.NewStreamBuffer(1024),
	}
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

func (sc *OpenAIResponsesStreamConverter) ensureToolCallState(index int) *ResponsesOutputToolCallState {
	state := sc.toolCalls[index]
	if state == nil {
		outputIndex := index
		if sc.output.AssistantReserved() {
			outputIndex++
		}
		state = &ResponsesOutputToolCallState{OutputIndex: outputIndex}
		sc.toolCalls[index] = state
	}
	return state
}

func (sc *OpenAIResponsesStreamConverter) reserveAssistantOutput() {
	if sc.output.AssistantReserved() {
		return
	}
	sc.output.ReserveAssistant()
	for _, state := range sc.toolCalls {
		if state != nil && !state.Started {
			state.OutputIndex++
		}
	}
}

func (sc *OpenAIResponsesStreamConverter) forceStartToolCall(state *ResponsesOutputToolCallState) string {
	if state.Started {
		return ""
	}
	if strings.TrimSpace(state.Name) == "" {
		state.Name = "unknown"
	}
	return sc.output.StartToolCall(state, false)
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

func (sc *OpenAIResponsesStreamConverter) handleToolCallDeltas(toolCalls []any) string {
	var out bytes.Buffer

	if sc.output.AssistantStarted() && !sc.output.AssistantDone() {
		out.WriteString(sc.output.CompleteAssistantOutput(0))
	}

	for _, item := range toolCalls {
		toolCall, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index, ok := normalizeToolCallIndex(toolCall["index"])
		if !ok {
			continue
		}

		state := sc.ensureToolCallState(index)
		if callID, _ := toolCall["id"].(string); callID != "" {
			state.CallID = callID
		}

		function, _ := toolCall["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			state.Name = name
		}

		arguments, _ := function["arguments"].(string)
		hadStarted := state.Started
		if arguments != "" {
			_, _ = state.Arguments.WriteString(arguments)
		}
		out.WriteString(sc.output.StartToolCall(state, false))

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
				"created_at": time.Now().Unix(),
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
	tempBuf := make([]byte, 1024)
	nr, readErr := sc.reader.Read(tempBuf)
	if nr > 0 {
		sc.lineBuffer.AppendBytes(tempBuf[:nr])

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

			if after, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
				data := after
				if bytes.Equal(data, []byte("[DONE]")) {
					// Send done event
					if !sc.sentDone {
						sc.sentDone = true
						sc.buffer.AppendString(sc.output.CompleteAssistantOutput(0))
						sc.buffer.AppendString(sc.completePendingToolCalls())
						responseData := map[string]any{
							"id":         sc.responseID,
							"object":     "response",
							"status":     "completed",
							"model":      sc.model,
							"provider":   sc.provider,
							"created_at": time.Now().Unix(),
						}
						// Include usage data if captured from OpenAI stream
						if sc.cachedUsage != nil {
							responseData["usage"] = sc.cachedUsage
						}
						doneEvent := map[string]any{
							"type":     "response.completed",
							"response": responseData,
						}
						jsonData, err := json.Marshal(doneEvent)
						if err != nil {
							slog.Error("failed to marshal response.completed event", "error", err, "response_id", sc.responseID)
							continue
						}
						sc.buffer.AppendString("event: response.completed\ndata: ")
						sc.buffer.AppendBytes(jsonData)
						sc.buffer.AppendString("\n\ndata: [DONE]\n\n")
					}
					continue
				}

				// Parse the chat completion chunk
				var chunk map[string]any
				if err := json.Unmarshal(data, &chunk); err != nil {
					continue
				}

				// Capture usage data if present (OpenAI sends this in the final chunk)
				if usage, ok := chunk["usage"].(map[string]any); ok {
					sc.cachedUsage = usage
				}

				// Extract content delta
				if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]any); ok {
						if delta, ok := choice["delta"].(map[string]any); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								sc.reserveAssistantOutput()
								sc.buffer.AppendString(sc.output.StartAssistantOutput(0))
								sc.output.AppendAssistantText(content)
								deltaEvent := map[string]any{
									"type":  "response.output_text.delta",
									"delta": content,
								}
								jsonData, err := json.Marshal(deltaEvent)
								if err != nil {
									slog.Error("failed to marshal content delta event", "error", err, "response_id", sc.responseID)
									continue
								}
								sc.buffer.AppendString("event: response.output_text.delta\ndata: ")
								sc.buffer.AppendBytes(jsonData)
								sc.buffer.AppendString("\n\n")
							}
							if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
								sc.buffer.AppendString(sc.handleToolCallDeltas(toolCalls))
							}
						}
						if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
							sc.buffer.AppendString(sc.completePendingToolCalls())
						}
					}
				}
			}
		}
	}

	if readErr != nil {
		if readErr == io.EOF {
			// Send final done event if we haven't already
			if !sc.sentDone {
				sc.sentDone = true
				sc.buffer.AppendString(sc.output.CompleteAssistantOutput(0))
				sc.buffer.AppendString(sc.completePendingToolCalls())
				responseData := map[string]any{
					"id":         sc.responseID,
					"object":     "response",
					"status":     "completed",
					"model":      sc.model,
					"provider":   sc.provider,
					"created_at": time.Now().Unix(),
				}
				// Include usage data if captured from OpenAI stream
				if sc.cachedUsage != nil {
					responseData["usage"] = sc.cachedUsage
				}
				doneEvent := map[string]any{
					"type":     "response.completed",
					"response": responseData,
				}
				jsonData, err := json.Marshal(doneEvent)
				if err != nil {
					slog.Error("failed to marshal final response.completed event", "error", err, "response_id", sc.responseID)
				} else {
					sc.buffer.AppendString("event: response.completed\ndata: ")
					sc.buffer.AppendBytes(jsonData)
					sc.buffer.AppendString("\n\ndata: [DONE]\n\n")
				}
			}

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
