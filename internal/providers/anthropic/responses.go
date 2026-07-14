package anthropic

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// convertAnthropicResponseToResponses converts an Anthropic response to ResponsesResponse
func convertAnthropicResponseToResponses(resp *anthropicResponse, model string) *core.ResponsesResponse {
	content := extractTextContent(resp.Content)
	toolCalls := extractToolCalls(resp.Content)

	msg := core.Message{
		Content:   content,
		ToolCalls: toolCalls,
	}
	output := providers.BuildResponsesOutputItems(core.ResponseMessage{
		Role:      "assistant",
		Content:   msg.Content,
		ToolCalls: msg.ToolCalls,
	})

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output:    output,
		Usage:     buildAnthropicResponsesUsage(resp.Usage),
	}
}

// buildAnthropicResponsesUsage creates a ResponsesUsage from anthropicUsage, including RawUsage.
func buildAnthropicResponsesUsage(u anthropicUsage) *core.ResponsesUsage {
	usage := &core.ResponsesUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.InputTokens + u.OutputTokens,
	}
	rawUsage := buildAnthropicRawUsage(u)
	if len(rawUsage) > 0 {
		usage.RawUsage = rawUsage
	}
	return usage
}

func anthropicResponsesUsagePayload(usage *anthropicUsage) map[string]any {
	if usage == nil {
		return nil
	}

	payload := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadInputTokens > 0 {
		payload["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens > 0 {
		payload["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
	}
	return payload
}

// Responses sends a Responses API request to Anthropic (converted to messages format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}

	var anthropicResp anthropicResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	}, &anthropicResp)
	if err != nil {
		return nil, err
	}

	return convertAnthropicResponseToResponses(&anthropicResp, req.Model), nil
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}
	anthropicReq.Stream = true

	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	})
	if err != nil {
		return nil, err
	}

	// Return a reader that converts Anthropic SSE format to Responses API format
	return newResponsesStreamConverter(stream, req.Model), nil
}

// responsesStreamConverter wraps an Anthropic stream and converts it to Responses API format
type responsesStreamConverter struct {
	reader          *bufio.Reader
	body            io.ReadCloser
	model           string
	responseID      string
	createdAt       int64
	output          *providers.ResponsesOutputEventState
	nextOutputIndex int
	toolCalls       map[int]*providers.ResponsesOutputToolCallState
	thinkingBlocks  map[int]bool // tracks which content block indices are thinking blocks
	buffer          streaming.StreamBuffer
	closed          bool
	sentDone        bool
	usage           anthropicUsage
	hasUsage        bool
}

func newResponsesStreamConverter(body io.ReadCloser, model string) *responsesStreamConverter {
	responseID := "resp_" + uuid.New().String()
	return &responsesStreamConverter{
		reader:         bufio.NewReader(body),
		body:           body,
		model:          model,
		responseID:     responseID,
		createdAt:      time.Now().Unix(),
		output:         providers.NewResponsesOutputEventState(responseID),
		toolCalls:      make(map[int]*providers.ResponsesOutputToolCallState),
		thinkingBlocks: make(map[int]bool),
		buffer:         streaming.NewStreamBuffer(1024),
	}
}

func (sc *responsesStreamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		sc.releaseBuffer()
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	// Read the next SSE event from Anthropic
	for {
		line, err := sc.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// Send final done event and [DONE] message
				if !sc.sentDone {
					sc.sentDone = true
					prefix := sc.output.CompleteAssistantOutput(0)
					responseData := map[string]any{
						"id":         sc.responseID,
						"object":     "response",
						"status":     "completed",
						"model":      sc.model,
						"provider":   "anthropic",
						"created_at": sc.createdAt,
					}
					// Include merged usage data captured across message_start/message_delta.
					if sc.hasUsage {
						responseData["usage"] = anthropicResponsesUsagePayload(&sc.usage)
					}
					doneEvent := map[string]any{
						"type":     "response.completed",
						"response": responseData,
					}
					jsonData, marshalErr := json.Marshal(doneEvent)
					if marshalErr != nil {
						slog.Error("failed to marshal response.completed event", "error", marshalErr, "response_id", sc.responseID)
						sc.closed = true
						sc.releaseBuffer()
						_ = sc.body.Close() //nolint:errcheck
						return 0, io.EOF
					}
					sc.buffer.AppendString(prefix)
					sc.buffer.AppendString("event: response.completed\ndata: ")
					sc.buffer.AppendBytes(jsonData)
					sc.buffer.AppendString("\n\ndata: [DONE]\n\n")
					return sc.buffer.Read(p), nil
				}
				sc.closed = true
				sc.releaseBuffer()
				_ = sc.body.Close() //nolint:errcheck
				return 0, io.EOF
			}
			return 0, err
		}

		n, handled, err := consumeAnthropicSSELine(p, line, sc.body, &sc.buffer, sc.convertEvent)
		if err != nil {
			sc.closed = true
			sc.releaseBuffer()
			return 0, err
		}
		if handled {
			if n == 0 {
				continue
			}
			return n, nil
		}
	}
}

func (sc *responsesStreamConverter) Close() error {
	if sc.closed {
		sc.releaseBuffer()
		return nil
	}
	sc.closed = true
	sc.releaseBuffer()
	return sc.body.Close()
}

func (sc *responsesStreamConverter) releaseBuffer() {
	sc.buffer.Release()
}

func (sc *responsesStreamConverter) reserveAssistantMessageOutput() {
	if sc.output.AssistantReserved() {
		return
	}
	sc.output.ReserveAssistant()
	sc.nextOutputIndex++
}

func (sc *responsesStreamConverter) newResponsesToolCallState(contentBlock *anthropicContent) *providers.ResponsesOutputToolCallState {
	callID := providers.ResponsesFunctionCallCallID(contentBlock.ID)
	state := &providers.ResponsesOutputToolCallState{
		CallID:      callID,
		Name:        contentBlock.Name,
		OutputIndex: sc.nextOutputIndex,
	}
	sc.nextOutputIndex++

	initialArguments := extractInitialToolArguments(contentBlock.Input)
	state.PlaceholderObject = initialArguments == "{}"
	if initialArguments != "" && !state.PlaceholderObject {
		_, _ = state.Arguments.WriteString(initialArguments)
	}

	return state
}

func (sc *responsesStreamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if mergeAnthropicUsage(&sc.usage, &event.Message.Usage) {
				sc.hasUsage = true
			}
		}
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		// Send response.created event
		return sc.output.WriteEvent("response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":         sc.responseID,
				"object":     "response",
				"status":     "in_progress",
				"model":      sc.model,
				"provider":   "anthropic",
				"created_at": sc.createdAt,
			},
		})

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "thinking" {
			sc.thinkingBlocks[event.Index] = true
			return ""
		}
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			if sc.output.AssistantStarted() && !sc.output.AssistantDone() {
				prefix := sc.output.CompleteAssistantOutput(0)
				state := sc.newResponsesToolCallState(event.ContentBlock)
				sc.toolCalls[event.Index] = state
				return prefix + sc.output.StartToolCall(state, true)
			}
			state := sc.newResponsesToolCallState(event.ContentBlock)
			sc.toolCalls[event.Index] = state
			return sc.output.StartToolCall(state, true)
		}
		return ""

	case "content_block_delta":
		if event.Delta == nil {
			return ""
		}

		switch event.Delta.Type {
		case "thinking_delta", "signature_delta":
			// Thinking and signature deltas are part of Anthropic's extended thinking;
			// the Responses API format does not have a direct equivalent, so skip them.
			return ""
		case "text_delta":
			if event.Delta.Text != "" {
				sc.reserveAssistantMessageOutput()
				prefix := sc.output.StartAssistantOutput(0)
				sc.output.AppendAssistantText(event.Delta.Text)
				return prefix + sc.output.WriteEvent("response.output_text.delta", map[string]any{
					"type":  "response.output_text.delta",
					"delta": event.Delta.Text,
				})
			}
		case "input_json_delta":
			if event.Delta.PartialJSON == "" {
				return ""
			}
			state := sc.toolCalls[event.Index]
			if state == nil {
				return ""
			}
			if state.PlaceholderObject {
				state.Arguments = strings.Builder{}
				state.PlaceholderObject = false
			}
			_, _ = state.Arguments.WriteString(event.Delta.PartialJSON)
			return sc.output.WriteEvent("response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"item_id":      state.ItemID,
				"output_index": state.OutputIndex,
				"delta":        event.Delta.PartialJSON,
			})
		}
		return ""

	case "content_block_stop":
		state := sc.toolCalls[event.Index]
		return sc.output.CompleteToolCall(state, true)

	case "message_delta":
		// Capture usage data for inclusion in response.completed
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		if !sc.output.AssistantReserved() && len(sc.toolCalls) == 0 {
			sc.reserveAssistantMessageOutput()
		}
		return ""

	case "message_stop":
		// Will be handled in Read() when we get EOF
		return ""
	}

	return ""
}
