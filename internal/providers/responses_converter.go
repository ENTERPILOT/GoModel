package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
)

// OpenAIResponsesStreamConverter wraps an OpenAI-compatible SSE stream
// and converts it to Responses API format.
// Used by providers that have OpenAI-compatible streaming (Groq, Gemini, etc.)
type OpenAIResponsesStreamConverter struct {
	reader                  io.ReadCloser
	model                   string
	provider                string
	responseID              string
	toolCalls               map[int]*openAIResponsesToolCallState
	buffer                  []byte
	lineBuffer              []byte
	closed                  bool
	sentCreate              bool
	sentDone                bool
	assistantOutputReserved bool
	cachedUsage             map[string]interface{} // Stores usage from final chunk for inclusion in response.completed
}

type openAIResponsesToolCallState struct {
	ItemID      string
	CallID      string
	Name        string
	Arguments   bytes.Buffer
	OutputIndex int
	Started     bool
	Completed   bool
}

// NewOpenAIResponsesStreamConverter creates a new converter that transforms
// OpenAI-format SSE streams to Responses API format.
func NewOpenAIResponsesStreamConverter(reader io.ReadCloser, model, provider string) *OpenAIResponsesStreamConverter {
	return &OpenAIResponsesStreamConverter{
		reader:     reader,
		model:      model,
		provider:   provider,
		responseID: "resp_" + uuid.New().String(),
		toolCalls:  make(map[int]*openAIResponsesToolCallState),
		buffer:     make([]byte, 0, 4096),
		lineBuffer: make([]byte, 0, 1024),
	}
}

func (sc *OpenAIResponsesStreamConverter) writeEvent(eventName string, payload map[string]interface{}) string {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		slog.Error("failed to marshal responses stream event", "error", err, "event", eventName, "response_id", sc.responseID)
		return ""
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventName, jsonData)
}

func normalizeToolCallIndex(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func (sc *OpenAIResponsesStreamConverter) renderToolCallItem(state *openAIResponsesToolCallState, status string) map[string]interface{} {
	return map[string]interface{}{
		"id":        state.ItemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   state.CallID,
		"name":      state.Name,
		"arguments": state.Arguments.String(),
	}
}

func (sc *OpenAIResponsesStreamConverter) ensureToolCallState(index int) *openAIResponsesToolCallState {
	state := sc.toolCalls[index]
	if state == nil {
		outputIndex := index
		if sc.assistantOutputReserved {
			outputIndex++
		}
		state = &openAIResponsesToolCallState{OutputIndex: outputIndex}
		sc.toolCalls[index] = state
	}
	return state
}

func (sc *OpenAIResponsesStreamConverter) reserveAssistantOutput() {
	if sc.assistantOutputReserved {
		return
	}
	sc.assistantOutputReserved = true
	for _, state := range sc.toolCalls {
		if state != nil && !state.Started {
			state.OutputIndex++
		}
	}
}

func (sc *OpenAIResponsesStreamConverter) startToolCall(state *openAIResponsesToolCallState) string {
	if state.Started {
		return ""
	}
	state.CallID = ResponsesFunctionCallCallID(state.CallID)
	state.ItemID = ResponsesFunctionCallItemID(state.CallID)
	state.Started = true
	return sc.writeEvent("response.output_item.added", map[string]interface{}{
		"type":         "response.output_item.added",
		"item":         sc.renderToolCallItem(state, "in_progress"),
		"output_index": state.OutputIndex,
	})
}

func (sc *OpenAIResponsesStreamConverter) completePendingToolCalls() string {
	indices := make([]int, 0, len(sc.toolCalls))
	for index := range sc.toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)

	var out bytes.Buffer
	for _, index := range indices {
		state := sc.toolCalls[index]
		if state == nil || !state.Started || state.Completed {
			continue
		}
		state.Completed = true
		out.WriteString(sc.writeEvent("response.function_call_arguments.done", map[string]interface{}{
			"type":         "response.function_call_arguments.done",
			"item_id":      state.ItemID,
			"output_index": state.OutputIndex,
			"arguments":    state.Arguments.String(),
		}))
		out.WriteString(sc.writeEvent("response.output_item.done", map[string]interface{}{
			"type":         "response.output_item.done",
			"item":         sc.renderToolCallItem(state, "completed"),
			"output_index": state.OutputIndex,
		}))
	}

	return out.String()
}

func (sc *OpenAIResponsesStreamConverter) handleToolCallDeltas(toolCalls []interface{}) string {
	var out bytes.Buffer

	for _, item := range toolCalls {
		toolCall, ok := item.(map[string]interface{})
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

		function, _ := toolCall["function"].(map[string]interface{})
		if name, _ := function["name"].(string); name != "" {
			state.Name = name
		}

		out.WriteString(sc.startToolCall(state))

		if arguments, _ := function["arguments"].(string); arguments != "" {
			_, _ = state.Arguments.WriteString(arguments)
			out.WriteString(sc.writeEvent("response.function_call_arguments.delta", map[string]interface{}{
				"type":         "response.function_call_arguments.delta",
				"item_id":      state.ItemID,
				"output_index": state.OutputIndex,
				"delta":        arguments,
			}))
		}
	}

	return out.String()
}

func (sc *OpenAIResponsesStreamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if len(sc.buffer) > 0 {
		n = copy(p, sc.buffer)
		sc.buffer = sc.buffer[n:]
		return n, nil
	}

	// Send response.created event first
	if !sc.sentCreate {
		sc.sentCreate = true
		createdEvent := map[string]interface{}{
			"type": "response.created",
			"response": map[string]interface{}{
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
		created := fmt.Sprintf("event: response.created\ndata: %s\n\n", jsonData)
		sc.buffer = append(sc.buffer, []byte(created)...)
		n = copy(p, sc.buffer)
		sc.buffer = sc.buffer[n:]
		return n, nil
	}

	// Read from the underlying stream
	tempBuf := make([]byte, 1024)
	nr, readErr := sc.reader.Read(tempBuf)
	if nr > 0 {
		sc.lineBuffer = append(sc.lineBuffer, tempBuf[:nr]...)

		// Process complete lines
		for {
			idx := bytes.Index(sc.lineBuffer, []byte("\n"))
			if idx == -1 {
				break
			}

			line := sc.lineBuffer[:idx]
			sc.lineBuffer = sc.lineBuffer[idx+1:]

			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			if bytes.HasPrefix(line, []byte("data: ")) {
				data := bytes.TrimPrefix(line, []byte("data: "))
				if bytes.Equal(data, []byte("[DONE]")) {
					// Send done event
					if !sc.sentDone {
						sc.sentDone = true
						sc.buffer = append(sc.buffer, []byte(sc.completePendingToolCalls())...)
						responseData := map[string]interface{}{
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
						doneEvent := map[string]interface{}{
							"type":     "response.completed",
							"response": responseData,
						}
						jsonData, err := json.Marshal(doneEvent)
						if err != nil {
							slog.Error("failed to marshal response.completed event", "error", err, "response_id", sc.responseID)
							continue
						}
						doneMsg := fmt.Sprintf("event: response.completed\ndata: %s\n\ndata: [DONE]\n\n", jsonData)
						sc.buffer = append(sc.buffer, []byte(doneMsg)...)
					}
					continue
				}

				// Parse the chat completion chunk
				var chunk map[string]interface{}
				if err := json.Unmarshal(data, &chunk); err != nil {
					continue
				}

				// Capture usage data if present (OpenAI sends this in the final chunk)
				if usage, ok := chunk["usage"].(map[string]interface{}); ok {
					sc.cachedUsage = usage
				}

				// Extract content delta
				if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								sc.reserveAssistantOutput()
								deltaEvent := map[string]interface{}{
									"type":  "response.output_text.delta",
									"delta": content,
								}
								jsonData, err := json.Marshal(deltaEvent)
								if err != nil {
									slog.Error("failed to marshal content delta event", "error", err, "response_id", sc.responseID)
									continue
								}
								sc.buffer = append(sc.buffer, []byte(fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", jsonData))...)
							}
							if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
								sc.buffer = append(sc.buffer, []byte(sc.handleToolCallDeltas(toolCalls))...)
							}
						}
						if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
							sc.buffer = append(sc.buffer, []byte(sc.completePendingToolCalls())...)
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
				sc.buffer = append(sc.buffer, []byte(sc.completePendingToolCalls())...)
				responseData := map[string]interface{}{
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
				doneEvent := map[string]interface{}{
					"type":     "response.completed",
					"response": responseData,
				}
				jsonData, err := json.Marshal(doneEvent)
				if err != nil {
					slog.Error("failed to marshal final response.completed event", "error", err, "response_id", sc.responseID)
				} else {
					doneMsg := fmt.Sprintf("event: response.completed\ndata: %s\n\ndata: [DONE]\n\n", jsonData)
					sc.buffer = append(sc.buffer, []byte(doneMsg)...)
				}
			}

			if len(sc.buffer) > 0 {
				n = copy(p, sc.buffer)
				sc.buffer = sc.buffer[n:]
				return n, nil
			}

			sc.closed = true
			_ = sc.reader.Close()
			return 0, io.EOF
		}
		return 0, readErr
	}

	if len(sc.buffer) > 0 {
		n = copy(p, sc.buffer)
		sc.buffer = sc.buffer[n:]
		return n, nil
	}

	// No data yet, try again
	return 0, nil
}

func (sc *OpenAIResponsesStreamConverter) Close() error {
	sc.closed = true
	return sc.reader.Close()
}
