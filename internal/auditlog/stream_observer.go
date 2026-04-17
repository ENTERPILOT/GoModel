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
	for w != nil {
		if _, ok := w.(*responseBodyCapture); ok {
			return true
		}
		unwrapper, ok := w.(responseWriterUnwrapper)
		if !ok {
			return false
		}
		w = unwrapper.Unwrap()
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

	if builder.ID == "" {
		if id, ok := event["id"].(string); ok {
			builder.ID = id
		}
		if model, ok := event["model"].(string); ok {
			builder.Model = model
		}
		if created, ok := event["created"].(float64); ok {
			builder.Created = int64(created)
		}
	}

	if choices, ok := event["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				builder.FinishReason = fr
			}

			if delta, ok := choice["delta"].(map[string]any); ok {
				if role, ok := delta["role"].(string); ok {
					builder.Role = role
				}
				if content, ok := delta["content"].(string); ok && content != "" {
					appendStreamContent(builder, content)
				}
			}
		}
	}
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
	if builder == nil || builder.truncated || builder.contentLen >= MaxContentCapture {
		return
	}

	remaining := MaxContentCapture - builder.contentLen
	if len(content) > remaining {
		content = content[:remaining]
		builder.truncated = true
	}
	builder.Content.WriteString(content)
	builder.contentLen += len(content)
}
