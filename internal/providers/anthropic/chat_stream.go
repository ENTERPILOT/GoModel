package anthropic

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/streaming"
)

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertToAnthropicRequest(req)
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

	// Return a reader that converts Anthropic SSE format to OpenAI format
	return newStreamConverter(stream, req.Model), nil
}

// streamConverter wraps an Anthropic stream and converts it to OpenAI format
type streamConverter struct {
	reader            *bufio.Reader
	body              io.ReadCloser
	model             string
	msgID             string
	created           int64
	nextToolCallIndex int
	toolCalls         map[int]*streamToolCallState
	thinkingBlocks    map[int]bool // tracks which content block indices are thinking blocks
	usage             anthropicUsage
	hasUsage          bool
	buffer            streaming.StreamBuffer
	closed            bool
	emittedToolCalls  bool
}

// streamToolCallState tracks per-tool-call bookkeeping for the chat dialect.
// Unlike the Responses converter, the chat path relays argument deltas
// verbatim and never needs the accumulated argument text, so none is kept.
type streamToolCallState struct {
	ID                string
	Name              string
	Index             int
	Started           bool
	PlaceholderObject bool
}

func newStreamConverter(body io.ReadCloser, model string) *streamConverter {
	return &streamConverter{
		reader:         bufio.NewReader(body),
		body:           body,
		model:          model,
		created:        time.Now().Unix(),
		toolCalls:      make(map[int]*streamToolCallState),
		thinkingBlocks: make(map[int]bool),
		buffer:         streaming.NewStreamBuffer(1024),
	}
}

func anthropicChatUsagePayload(usage *anthropicUsage) map[string]any {
	if usage == nil {
		return nil
	}

	payload := map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadInputTokens > 0 {
		payload["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens > 0 {
		payload["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
	}
	return payload
}

func (sc *streamConverter) Read(p []byte) (n int, err error) {
	// If we have buffered data, return it first
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	if sc.closed {
		sc.releaseBuffer()
		return 0, io.EOF
	}

	// Read the next SSE event from Anthropic
	for {
		line, err := sc.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// Send final [DONE] message
				sc.buffer.AppendString("data: [DONE]\n\n")
				n = sc.buffer.Read(p)
				sc.closed = true
				_ = sc.body.Close() //nolint:errcheck
				return n, nil
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

func (sc *streamConverter) Close() error {
	if sc.closed {
		sc.releaseBuffer()
		return nil
	}
	sc.closed = true
	sc.releaseBuffer()
	return sc.body.Close()
}

func (sc *streamConverter) releaseBuffer() {
	sc.buffer.Release()
}

func (sc *streamConverter) mapStreamStopReason(reason string) string {
	// Preserve raw "tool_use" when the upstream stream never produced any
	// tool call deltas. This avoids claiming OpenAI-style tool calls for a
	// malformed or partial Anthropic stream.
	if reason == "tool_use" && !sc.emittedToolCalls {
		return reason
	}
	return normalizeAnthropicStopReason(reason)
}

func (sc *streamConverter) formatChatChunk(delta map[string]any, finishReason any, usage *anthropicUsage) string {
	return providers.FormatChatChunkSSE(sc.msgID, sc.created, sc.model, "anthropic", delta, finishReason, anthropicChatUsagePayload(usage))
}

func (sc *streamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		role := ""
		if event.Message != nil {
			sc.msgID = event.Message.ID
			if mergeAnthropicUsage(&sc.usage, &event.Message.Usage) {
				sc.hasUsage = true
			}
			role = strings.TrimSpace(event.Message.Role)
		}
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		if event.Message != nil {
			if role == "" {
				role = "assistant"
			}
			return sc.formatChatChunk(map[string]any{
				"role": role,
			}, nil, nil)
		}
		return ""

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "thinking" {
			sc.thinkingBlocks[event.Index] = true
			return ""
		}
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			state := &streamToolCallState{
				ID:    event.ContentBlock.ID,
				Name:  event.ContentBlock.Name,
				Index: sc.nextToolCallIndex,
			}
			sc.nextToolCallIndex++

			initialArguments := extractInitialToolArguments(event.ContentBlock.Input)
			state.PlaceholderObject = initialArguments == "{}"
			if state.PlaceholderObject {
				sc.toolCalls[event.Index] = state
				return ""
			}
			state.Started = true
			sc.toolCalls[event.Index] = state
			sc.emittedToolCalls = true

			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"id":    state.ID,
						"type":  "function",
						"function": map[string]any{
							"name":      state.Name,
							"arguments": initialArguments,
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "content_block_delta":
		if event.Delta == nil {
			return ""
		}

		switch event.Delta.Type {
		case "thinking_delta":
			if sc.thinkingBlocks[event.Index] && event.Delta.Thinking != "" {
				return sc.formatChatChunk(map[string]any{
					"reasoning_content": event.Delta.Thinking,
				}, nil, nil)
			}
		case "signature_delta":
			// Signature deltas are internal to Anthropic's thinking protocol;
			// no OpenAI-compatible equivalent to emit.
			return ""
		case "text_delta":
			if event.Delta.Text != "" {
				return sc.formatChatChunk(map[string]any{
					"content": event.Delta.Text,
				}, nil, nil)
			}
		case "input_json_delta":
			if event.Delta.PartialJSON == "" {
				return ""
			}
			state := sc.toolCalls[event.Index]
			if state == nil {
				return ""
			}
			state.PlaceholderObject = false
			if !state.Started {
				state.Started = true
				sc.emittedToolCalls = true
				return sc.formatChatChunk(map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": state.Index,
							"id":    state.ID,
							"type":  "function",
							"function": map[string]any{
								"name":      state.Name,
								"arguments": event.Delta.PartialJSON,
							},
						},
					},
				}, nil, nil)
			}
			sc.emittedToolCalls = true
			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"function": map[string]any{
							"arguments": event.Delta.PartialJSON,
						},
					},
				},
			}, nil, nil)
		}

	case "content_block_stop":
		state := sc.toolCalls[event.Index]
		if state != nil && !state.Started && state.PlaceholderObject {
			state.Started = true
			sc.emittedToolCalls = true
			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"id":    state.ID,
						"type":  "function",
						"function": map[string]any{
							"name":      state.Name,
							"arguments": "{}",
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "message_delta":
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		// Emit chunk if we have stop_reason or usage data
		if (event.Delta != nil && event.Delta.StopReason != "") || event.Usage != nil {
			var finishReason any
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = sc.mapStreamStopReason(event.Delta.StopReason)
			}
			var usage *anthropicUsage
			if sc.hasUsage {
				usage = &sc.usage
			}
			return sc.formatChatChunk(map[string]any{}, finishReason, usage)
		}

	case "message_stop":
		return ""
	}

	return ""
}
