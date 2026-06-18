package anthropicapi

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"

	"gomodel/internal/streaming"
)

// chatChunk is the subset of an OpenAI chat.completion.chunk consumed by the
// stream converter.
type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			ToolCalls        []chatToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	PromptTokensDetails      *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u chatUsage) cacheRead() int {
	if u.CacheReadInputTokens > 0 {
		return u.CacheReadInputTokens
	}
	if u.PromptTokensDetails != nil {
		return u.PromptTokensDetails.CachedTokens
	}
	return 0
}

// NewStreamConverter wraps an OpenAI-style chat completion SSE stream and emits
// the equivalent Anthropic Messages SSE event sequence. The returned reader
// owns body and closes it on Close.
func NewStreamConverter(body io.ReadCloser, model string) io.ReadCloser {
	return &streamConverter{
		reader:    bufio.NewReader(body),
		body:      body,
		model:     model,
		buffer:    streaming.NewStreamBuffer(1024),
		toolBlock: make(map[int]int),
	}
}

// streamConverter is an io.ReadCloser that converts a chat SSE stream into an
// Anthropic Messages SSE stream. It is single-reader and must not be shared.
type streamConverter struct {
	reader *bufio.Reader
	body   io.ReadCloser
	buffer streaming.StreamBuffer
	model  string

	started    bool
	blockOpen  bool
	blockType  string
	curIndex   int
	nextIndex  int
	toolBlock  map[int]int
	stopReason string
	usage      chatUsage
	finalized  bool
	closed     bool
}

func (sc *streamConverter) Read(p []byte) (int, error) {
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}
	// End-of-stream is signalled by io.EOF only. Ownership of the underlying
	// stream stays with Close so observers (audit, usage) still fire on
	// OnStreamClose; Read must never mark the converter closed.
	if sc.finalized || sc.closed {
		return 0, io.EOF
	}

	for {
		line, err := sc.reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if done := sc.consumeLine(line); done {
				sc.finalize()
			}
		}
		if err != nil {
			if err == io.EOF {
				sc.finalize()
				if sc.buffer.Len() > 0 {
					return sc.buffer.Read(p), nil
				}
				return 0, io.EOF
			}
			return 0, err
		}
		if sc.buffer.Len() > 0 {
			return sc.buffer.Read(p), nil
		}
		if sc.finalized {
			return 0, io.EOF
		}
	}
}

func (sc *streamConverter) Close() error {
	if sc.closed {
		return nil
	}
	sc.closed = true
	sc.buffer.Release()
	return sc.body.Close()
}

// consumeLine parses a single SSE line. It returns true when the stream's
// terminal [DONE] sentinel is seen.
func (sc *streamConverter) consumeLine(line []byte) (done bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}
	var chunk chatChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return false
	}
	sc.handleChunk(&chunk)
	return false
}

func (sc *streamConverter) handleChunk(chunk *chatChunk) {
	sc.ensureStarted(chunk.ID, chunk.Model)
	if chunk.Usage != nil {
		sc.usage = *chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent != "" {
			sc.ensureBlock("thinking")
			sc.emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": sc.curIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": choice.Delta.ReasoningContent},
			})
		}
		if choice.Delta.Content != "" {
			sc.ensureBlock("text")
			sc.emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": sc.curIndex,
				"delta": map[string]any{"type": "text_delta", "text": choice.Delta.Content},
			})
		}
		for _, call := range choice.Delta.ToolCalls {
			sc.handleToolCall(call)
		}
		if choice.FinishReason != "" {
			sc.stopReason = stopReasonFromFinish(choice.FinishReason, len(sc.toolBlock) > 0)
		}
	}
}

func (sc *streamConverter) handleToolCall(call chatToolCallDelta) {
	index, seen := sc.toolBlock[call.Index]
	if !seen {
		sc.closeBlock()
		sc.openBlock("tool_use", map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Function.Name,
			"input": map[string]any{},
		})
		sc.toolBlock[call.Index] = sc.curIndex
		index = sc.curIndex
	}
	if call.Function.Arguments == "" {
		return
	}
	sc.emit("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments},
	})
}

// ensureBlock opens a text or thinking content block, closing any block of a
// different type that is currently open.
func (sc *streamConverter) ensureBlock(blockType string) {
	if sc.blockOpen && sc.blockType == blockType {
		return
	}
	sc.closeBlock()
	contentBlock := map[string]any{"type": blockType}
	if blockType == "thinking" {
		contentBlock["thinking"] = ""
	} else {
		contentBlock["text"] = ""
	}
	sc.openBlock(blockType, contentBlock)
}

func (sc *streamConverter) openBlock(blockType string, contentBlock map[string]any) {
	sc.curIndex = sc.nextIndex
	sc.nextIndex++
	sc.blockOpen = true
	sc.blockType = blockType
	sc.emit("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         sc.curIndex,
		"content_block": contentBlock,
	})
}

func (sc *streamConverter) closeBlock() {
	if !sc.blockOpen {
		return
	}
	sc.blockOpen = false
	sc.emit("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": sc.curIndex,
	})
}

func (sc *streamConverter) ensureStarted(id, model string) {
	if sc.started {
		return
	}
	sc.started = true
	msgID := normalizeMessageID(id)
	if msgID == "" {
		msgID = "msg_stream"
	}
	if model == "" {
		model = sc.model
	}
	sc.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// finalize emits the closing message_delta and message_stop events exactly once.
func (sc *streamConverter) finalize() {
	if sc.finalized {
		return
	}
	sc.finalized = true
	sc.ensureStarted("", "")
	sc.closeBlock()

	stopReason := sc.stopReason
	if stopReason == "" {
		stopReason = "end_turn"
	}
	sc.emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": sc.usagePayload(),
	})
	sc.emit("message_stop", map[string]any{"type": "message_stop"})
}

func (sc *streamConverter) usagePayload() map[string]any {
	payload := map[string]any{"output_tokens": sc.usage.CompletionTokens}
	if sc.usage.PromptTokens > 0 {
		payload["input_tokens"] = sc.usage.PromptTokens
	}
	if read := sc.usage.cacheRead(); read > 0 {
		payload["cache_read_input_tokens"] = read
	}
	if sc.usage.CacheCreationInputTokens > 0 {
		payload["cache_creation_input_tokens"] = sc.usage.CacheCreationInputTokens
	}
	return payload
}

// emit appends a complete Anthropic SSE event to the output buffer.
func (sc *streamConverter) emit(eventType string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	sc.buffer.AppendString("event: " + eventType + "\ndata: ")
	sc.buffer.AppendBytes(data)
	sc.buffer.AppendString("\n\n")
}
