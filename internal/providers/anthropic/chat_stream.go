package anthropic

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertToAnthropicRequest(req)
	if err != nil {
		return nil, err
	}
	anthropicReq.Stream = true

	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/messages",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Body:      anthropicReq,
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
	// thinkingReplay accumulates the thinking blocks of this turn, keyed by
	// upstream content-block index and ordered by thinkingOrder, so the
	// signatures Anthropic requires back can be handed to the client.
	thinkingReplay   map[int]*anthropicContent
	thinkingOrder    []int
	usage            anthropicUsage
	hasUsage         bool
	buffer           streaming.StreamBuffer
	closed           bool
	emittedToolCalls bool
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
		thinkingReplay: make(map[int]*anthropicContent),
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
	addAnthropicUsagePayloadDetails(payload, usage, "completion_tokens_details")
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
			sc.trackThinking(event.Index, *event.ContentBlock)
			return ""
		}
		if event.ContentBlock != nil && event.ContentBlock.Type == "redacted_thinking" {
			// A redacted block arrives whole and has no readable text, so
			// replay state is the only thing that can carry it.
			sc.trackThinking(event.Index, *event.ContentBlock)
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
				if block := sc.thinkingReplay[event.Index]; block != nil {
					block.Thinking += event.Delta.Thinking
				}
				return sc.formatChatChunk(map[string]any{
					"reasoning_content": event.Delta.Thinking,
				}, nil, nil)
			}
		case "signature_delta":
			// A signature has no OpenAI-compatible field of its own; it is
			// held until the block closes and then handed over as replay
			// state, which is what the client must echo back.
			if block := sc.thinkingReplay[event.Index]; block != nil {
				block.Signature += event.Delta.Signature
			}
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
		// The completed thinking block is published before any text follows,
		// so a client that closes it on the first text delta still sees the
		// signature.
		if chunk := sc.thinkingReplayChunk(event.Index); chunk != "" {
			return chunk
		}
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
			delta := map[string]any{}
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = sc.mapStreamStopReason(event.Delta.StopReason)
				// Carry the matched stop sequence as a delta extension field:
				// OpenAI's finish_reason "stop" cannot express it.
				if event.Delta.StopSequence != "" {
					delta["stop_sequence"] = event.Delta.StopSequence
				}
			}
			var usage *anthropicUsage
			if sc.hasUsage {
				usage = &sc.usage
			}
			return sc.formatChatChunk(delta, finishReason, usage)
		}

	case "message_stop":
		return ""
	}

	return ""
}

// trackThinking records a thinking or redacted_thinking block so its
// signature, or its opaque payload, can be replayed on a later turn.
func (sc *streamConverter) trackThinking(index int, block anthropicContent) {
	if _, seen := sc.thinkingReplay[index]; seen {
		return
	}
	tracked := block
	sc.thinkingReplay[index] = &tracked
	sc.thinkingOrder = append(sc.thinkingOrder, index)
}

// thinkingReplayChunk emits the thinking blocks accumulated so far when the
// block at index closes. The value is cumulative rather than incremental so a
// client that keeps only the most recent extra_content still ends up with the
// whole turn.
func (sc *streamConverter) thinkingReplayChunk(index int) string {
	if _, ok := sc.thinkingReplay[index]; !ok {
		return ""
	}
	blocks := make([]json.RawMessage, 0, len(sc.thinkingOrder))
	for _, i := range sc.thinkingOrder {
		if raw, ok := thinkingReplayJSON(*sc.thinkingReplay[i]); ok {
			blocks = append(blocks, raw)
		}
	}
	raw, err := json.Marshal(map[string][]json.RawMessage{core.ThinkingBlocksField: blocks})
	if err != nil {
		return ""
	}
	vendors, err := json.Marshal(map[string]json.RawMessage{core.ExtraContentVendorAnthropic: raw})
	if err != nil {
		return ""
	}
	return sc.formatChatChunk(map[string]any{core.ExtraContentField: json.RawMessage(vendors)}, nil, nil)
}
