package auditlog

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/streaming"
)

type responseWriterUnwrapper interface {
	Unwrap() http.ResponseWriter
}

const maxResponseWriterUnwrapDepth = 10

// StreamLogObserver reconstructs stream metadata and optional response bodies
// from parsed SSE JSON payloads.
type StreamLogObserver struct {
	logger    LoggerInterface
	entry     *LogEntry
	builder   *streamResponseBuilder
	logBodies bool
	closed    bool
	startTime time.Time
}

func NewStreamLogObserver(logger LoggerInterface, entry *LogEntry, path string) *StreamLogObserver {
	if logger == nil || entry == nil {
		return nil
	}

	logBodies := logger.Config().LogBodies
	var builder *streamResponseBuilder
	if logBodies {
		builder = &streamResponseBuilder{
			IsResponsesAPI: strings.HasPrefix(path, "/v1/responses"),
		}
	}

	return &StreamLogObserver{
		logger:    logger,
		entry:     entry,
		builder:   builder,
		logBodies: logBodies,
		startTime: entry.Timestamp,
	}
}

func (o *StreamLogObserver) OnJSONEvent(event map[string]any) {
	if !o.logBodies || o.builder == nil {
		return
	}
	observeStreamJSONEvent(o.builder, event)
}

func (o *StreamLogObserver) OnStreamClose() {
	if o.closed {
		return
	}
	o.closed = true

	if o.entry != nil && !o.startTime.IsZero() {
		o.entry.DurationNs = time.Since(o.startTime).Nanoseconds()
	}

	if o.logBodies && o.builder != nil && o.entry != nil && o.entry.Data != nil {
		if o.builder.IsResponsesAPI {
			o.entry.Data.ResponseBody = o.builder.buildResponsesAPIResponse()
		} else {
			o.entry.Data.ResponseBody = o.builder.buildChatCompletionResponse()
		}
		o.entry.Data.ResponseBodyTooBigToHandle = o.builder.truncated
	}

	if o.logger != nil && o.entry != nil {
		o.logger.Write(o.entry)
	}
}

// EnrichEntryWithCachedStreamResponse reconstructs the OpenAI-compatible
// response body for a cached SSE replay when audit body capture is enabled.
func EnrichEntryWithCachedStreamResponse(c *echo.Context, path string, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	if !hasResponseBodyCapture(c.Response()) {
		return
	}

	entry := GetStreamEntryFromContext(c)
	if entry == nil {
		return
	}

	builder := &streamResponseBuilder{
		IsResponsesAPI: strings.HasPrefix(path, "/v1/responses"),
	}
	observer := &cachedStreamObserver{builder: builder}
	stream := streaming.NewObservedSSEStream(io.NopCloser(bytes.NewReader(body)), observer)
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()

	data := ensureLogData(entry)
	if builder.IsResponsesAPI {
		data.ResponseBody = builder.buildResponsesAPIResponse()
	} else {
		data.ResponseBody = builder.buildChatCompletionResponse()
	}
	data.ResponseBodyTooBigToHandle = builder.truncated
}

type cachedStreamObserver struct {
	builder *streamResponseBuilder
}

func (o *cachedStreamObserver) OnJSONEvent(event map[string]any) {
	if o == nil || o.builder == nil {
		return
	}
	observeStreamJSONEvent(o.builder, event)
}

func (o *cachedStreamObserver) OnStreamClose() {}

func hasResponseBodyCapture(w http.ResponseWriter) bool {
	for depth := 0; w != nil && depth < maxResponseWriterUnwrapDepth; depth++ {
		if _, ok := w.(*responseBodyCapture); ok {
			return true
		}
		unwrapper, ok := w.(responseWriterUnwrapper)
		if !ok {
			return false
		}
		next := unwrapper.Unwrap()
		if next == w {
			return false
		}
		w = next
	}
	return false
}

func observeStreamJSONEvent(builder *streamResponseBuilder, event map[string]any) {
	if builder == nil {
		return
	}
	if builder.IsResponsesAPI {
		parseResponsesAPIEvent(builder, event)
		return
	}
	parseChatCompletionEvent(builder, event)
}

func parseChatCompletionEvent(builder *streamResponseBuilder, event map[string]any) {
	if builder == nil {
		return
	}

	if id, ok := event["id"].(string); ok && id != "" {
		builder.ID = id
	}
	if model, ok := event["model"].(string); ok && model != "" {
		builder.Model = model
	}
	if provider, ok := event["provider"].(string); ok && provider != "" {
		builder.Provider = provider
	}
	if fingerprint, ok := event["system_fingerprint"].(string); ok && fingerprint != "" {
		builder.SystemFingerprint = fingerprint
	}
	if created, ok := jsonNumberToInt64(event["created"]); ok {
		builder.Created = created
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		builder.Usage = copyAnyMap(usage)
	}

	if choices, ok := event["choices"].([]any); ok && len(choices) > 0 {
		for _, choiceAny := range choices {
			choice, ok := choiceAny.(map[string]any)
			if !ok {
				continue
			}
			index, ok := jsonNumberToInt(choice["index"])
			if !ok {
				index = defaultChatChoiceIndex(builder.Choices)
			}
			state := builder.chatChoice(index)
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				state.FinishReason = fr
			}

			if delta, ok := choice["delta"].(map[string]any); ok {
				if role, ok := delta["role"].(string); ok {
					state.Role = role
				}
				if content, ok := delta["content"].(string); ok && content != "" {
					appendChatContent(builder, state, content)
				}
				appendChatToolCalls(builder, state, delta)
			}
		}
	}
}

func defaultChatChoiceIndex(states map[int]*streamChatChoiceState) int {
	return defaultStreamStateIndex(states)
}

func defaultStreamStateIndex[T any](states map[int]T) int {
	if len(states) == 1 {
		for index := range states {
			return index
		}
	}
	maxIndex := -1
	for index := range states {
		if index > maxIndex {
			maxIndex = index
		}
	}
	return maxIndex + 1
}

func appendChatContent(builder *streamResponseBuilder, state *streamChatChoiceState, content string) {
	if state == nil {
		return
	}
	appendLimitedStreamText(builder, &state.Content, content)
}

func appendChatToolCalls(builder *streamResponseBuilder, state *streamChatChoiceState, delta map[string]any) {
	if state == nil || delta == nil {
		return
	}

	toolCalls, ok := delta["tool_calls"].([]any)
	if !ok {
		return
	}
	for _, toolAny := range toolCalls {
		toolMap, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}
		toolIndex, ok := jsonNumberToInt(toolMap["index"])
		if !ok {
			toolIndex = defaultToolCallIndex(state.ToolCalls)
		}
		toolState := state.toolCall(toolIndex)
		if id, ok := toolMap["id"].(string); ok && id != "" && toolState.ID == "" {
			toolState.ID = id
		}
		if typ, ok := toolMap["type"].(string); ok && typ != "" && toolState.Type == "" {
			toolState.Type = typ
		}
		function, ok := toolMap["function"].(map[string]any)
		if !ok {
			continue
		}
		toolState.hasFunction = true
		if name, ok := function["name"].(string); ok && name != "" && toolState.Name == "" {
			toolState.Name = name
		}
		if arguments, ok := function["arguments"].(string); ok && arguments != "" {
			appendLimitedStreamText(builder, &toolState.Arguments, arguments)
		}
	}
}

func defaultToolCallIndex(states map[int]*streamChatToolCallState) int {
	return defaultStreamStateIndex(states)
}

func parseResponsesAPIEvent(builder *streamResponseBuilder, event map[string]any) {
	if builder == nil {
		return
	}

	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.created", "response.completed", "response.done":
		if resp, ok := event["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok {
				builder.ResponseID = id
			}
			if status, ok := resp["status"].(string); ok {
				builder.Status = status
			}
			if model, ok := resp["model"].(string); ok {
				builder.Model = model
			}
			if createdAt, ok := resp["created_at"].(float64); ok {
				builder.CreatedAt = int64(createdAt)
			}
		}
	case "response.output_text.delta":
		if delta, ok := event["delta"].(string); ok && delta != "" {
			appendStreamContent(builder, delta)
		}
	}
}

func appendStreamContent(builder *streamResponseBuilder, content string) {
	if builder == nil {
		return
	}
	appendLimitedStreamText(builder, &builder.OutputText, content)
}

func appendLimitedStreamText(builder *streamResponseBuilder, dst *strings.Builder, content string) {
	if builder == nil || dst == nil || content == "" || builder.truncated {
		return
	}

	remaining := MaxContentCapture - builder.contentLen
	if remaining <= 0 {
		builder.truncated = true
		return
	}
	if len(content) > remaining {
		content = content[:remaining]
		builder.truncated = true
	}
	dst.WriteString(content)
	builder.contentLen += len(content)
}
