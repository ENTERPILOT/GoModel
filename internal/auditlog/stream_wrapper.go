package auditlog

import (
	"io"
	"strings"

	"gomodel/internal/streaming"
)

// Note: MaxContentCapture and LogEntryStreamingKey constants are defined in constants.go

// streamResponseBuilder accumulates data from SSE events to reconstruct a response
type streamResponseBuilder struct {
	// ChatCompletion fields
	ID           string
	Model        string
	Created      int64
	Role         string
	FinishReason string
	Content      strings.Builder // accumulated delta content

	// Responses API fields
	IsResponsesAPI bool
	ResponseID     string
	CreatedAt      int64
	Status         string

	// Tracking
	contentLen int // track content length to enforce limit
	truncated  bool
}

// StreamLogWrapper wraps an io.ReadCloser to reconstruct streamed response bodies
// for audit logging.
type StreamLogWrapper struct {
	io.ReadCloser
}

// NewStreamLogWrapper creates a wrapper around a stream for audit logging.
// When the stream is closed, it logs the accumulated entry.
// The path parameter is used to detect whether this is a ChatCompletion or Responses API request.
func NewStreamLogWrapper(stream io.ReadCloser, logger LoggerInterface, entry *LogEntry, path string) *StreamLogWrapper {
	observer := NewStreamLogObserver(logger, entry, path)
	if observer == nil {
		return &StreamLogWrapper{ReadCloser: stream}
	}
	return &StreamLogWrapper{
		ReadCloser: streaming.NewObservedSSEStream(stream, observer),
	}
}

// buildChatCompletionResponse constructs a ChatCompletion response from accumulated data
func (b *streamResponseBuilder) buildChatCompletionResponse() map[string]interface{} {
	return map[string]interface{}{
		"id":      b.ID,
		"object":  "chat.completion",
		"model":   b.Model,
		"created": b.Created,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    b.Role,
					"content": b.Content.String(),
				},
				"finish_reason": b.FinishReason,
			},
		},
	}
}

// buildResponsesAPIResponse constructs a Responses API response from accumulated data
func (b *streamResponseBuilder) buildResponsesAPIResponse() map[string]interface{} {
	return map[string]interface{}{
		"id":         b.ResponseID,
		"object":     "response",
		"model":      b.Model,
		"created_at": b.CreatedAt,
		"status":     b.Status,
		"output": []map[string]interface{}{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type": "output_text",
						"text": b.Content.String(),
					},
				},
			},
		},
	}
}

// WrapStreamForLogging wraps a stream with logging if enabled.
// This is a convenience function for use in handlers.
// The path parameter is used to detect whether this is a ChatCompletion or Responses API request.
func WrapStreamForLogging(stream io.ReadCloser, logger LoggerInterface, entry *LogEntry, path string) io.ReadCloser {
	if logger == nil || !logger.Config().Enabled || entry == nil {
		return stream
	}
	return NewStreamLogWrapper(stream, logger, entry, path)
}

// CreateStreamEntry creates a new log entry for a streaming request.
// This should be called before starting the stream.
func CreateStreamEntry(baseEntry *LogEntry) *LogEntry {
	if baseEntry == nil {
		return nil
	}

	// Create a copy of the entry for the stream
	// The stream wrapper will complete and write it when the stream closes
	entryCopy := &LogEntry{
		ID:            baseEntry.ID,
		Timestamp:     baseEntry.Timestamp,
		DurationNs:    baseEntry.DurationNs,
		Model:         baseEntry.Model,
		ResolvedModel: baseEntry.ResolvedModel,
		Provider:      baseEntry.Provider,
		AliasUsed:     baseEntry.AliasUsed,
		StatusCode:    baseEntry.StatusCode,
		// Copy extracted fields
		RequestID: baseEntry.RequestID,
		ClientIP:  baseEntry.ClientIP,
		Method:    baseEntry.Method,
		Path:      baseEntry.Path,
		Stream:    true, // Mark as streaming
	}

	if baseEntry.Data != nil {
		entryCopy.Data = &LogData{
			UserAgent:       baseEntry.Data.UserAgent,
			APIKeyHash:      baseEntry.Data.APIKeyHash,
			Temperature:     baseEntry.Data.Temperature,
			MaxTokens:       baseEntry.Data.MaxTokens,
			RequestHeaders:  copyMap(baseEntry.Data.RequestHeaders),
			ResponseHeaders: copyMap(baseEntry.Data.ResponseHeaders),
			RequestBody:     baseEntry.Data.RequestBody,
		}
	}

	return entryCopy
}

// copyMap creates a shallow copy of a string map
func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// GetStreamEntryFromContext retrieves the log entry from Echo context for streaming.
// This allows handlers to get the entry for wrapping streams.
func GetStreamEntryFromContext(c interface{ Get(string) interface{} }) *LogEntry {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return nil
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok {
		return nil
	}

	return entry
}

// MarkEntryAsStreaming marks the entry as a streaming request so the middleware
// knows not to log it (the stream wrapper will handle logging).
func MarkEntryAsStreaming(c interface{ Set(string, interface{}) }, isStreaming bool) {
	c.Set(string(LogEntryStreamingKey), isStreaming)
}

// IsEntryMarkedAsStreaming checks if the entry is marked as streaming.
func IsEntryMarkedAsStreaming(c interface{ Get(string) interface{} }) bool {
	val := c.Get(string(LogEntryStreamingKey))
	if val == nil {
		return false
	}
	streaming, _ := val.(bool)
	return streaming
}
