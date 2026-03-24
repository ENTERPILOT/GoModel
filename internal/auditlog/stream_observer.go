package auditlog

import (
	"strings"
	"time"
)

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
	if o.builder.IsResponsesAPI {
		o.parseResponsesAPIEvent(event)
		return
	}
	o.parseChatCompletionEvent(event)
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

func (o *StreamLogObserver) parseChatCompletionEvent(event map[string]any) {
	if o.builder == nil {
		return
	}

	if o.builder.ID == "" {
		if id, ok := event["id"].(string); ok {
			o.builder.ID = id
		}
		if model, ok := event["model"].(string); ok {
			o.builder.Model = model
		}
		if created, ok := event["created"].(float64); ok {
			o.builder.Created = int64(created)
		}
	}

	if choices, ok := event["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				o.builder.FinishReason = fr
			}

			if delta, ok := choice["delta"].(map[string]any); ok {
				if role, ok := delta["role"].(string); ok {
					o.builder.Role = role
				}
				if content, ok := delta["content"].(string); ok && content != "" {
					o.appendContent(content)
				}
			}
		}
	}
}

func (o *StreamLogObserver) parseResponsesAPIEvent(event map[string]any) {
	if o.builder == nil {
		return
	}

	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.created", "response.completed", "response.done":
		if resp, ok := event["response"].(map[string]any); ok {
			if id, ok := resp["id"].(string); ok {
				o.builder.ResponseID = id
			}
			if status, ok := resp["status"].(string); ok {
				o.builder.Status = status
			}
			if model, ok := resp["model"].(string); ok {
				o.builder.Model = model
			}
			if createdAt, ok := resp["created_at"].(float64); ok {
				o.builder.CreatedAt = int64(createdAt)
			}
		}
	case "response.output_text.delta":
		if delta, ok := event["delta"].(string); ok && delta != "" {
			o.appendContent(delta)
		}
	}
}

func (o *StreamLogObserver) appendContent(content string) {
	if o.builder == nil || o.builder.truncated || o.builder.contentLen >= MaxContentCapture {
		return
	}

	remaining := MaxContentCapture - o.builder.contentLen
	if len(content) > remaining {
		content = content[:remaining]
		o.builder.truncated = true
	}
	o.builder.Content.WriteString(content)
	o.builder.contentLen += len(content)
}
