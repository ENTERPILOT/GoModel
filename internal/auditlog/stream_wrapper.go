package auditlog

import (
	"maps"
	"strings"
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

	// Anthropic Messages fields
	IsAnthropicMessages bool

	// Tracking
	contentLen int // track content length to enforce limit
	truncated  bool
}

// buildChatCompletionResponse constructs a ChatCompletion response from accumulated data
func (b *streamResponseBuilder) buildChatCompletionResponse() map[string]any {
	return map[string]any{
		"id":      b.ID,
		"object":  "chat.completion",
		"model":   b.Model,
		"created": b.Created,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    b.Role,
					"content": b.Content.String(),
				},
				"finish_reason": b.FinishReason,
			},
		},
	}
}

// buildResponsesAPIResponse constructs a Responses API response from accumulated data
func (b *streamResponseBuilder) buildResponsesAPIResponse() map[string]any {
	return map[string]any{
		"id":         b.ResponseID,
		"object":     "response",
		"model":      b.Model,
		"created_at": b.CreatedAt,
		"status":     b.Status,
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{
						"type": "output_text",
						"text": b.Content.String(),
					},
				},
			},
		},
	}
}

func (b *streamResponseBuilder) buildAnthropicMessageResponse() map[string]any {
	return map[string]any{
		"id":    b.ID,
		"type":  "message",
		"role":  b.Role,
		"model": b.Model,
		"content": []map[string]any{
			{
				"type": "text",
				"text": b.Content.String(),
			},
		},
		"stop_reason": b.FinishReason,
	}
}

// CreateStreamEntry creates a new log entry for a streaming request.
// This should be called before starting the stream.
func CreateStreamEntry(baseEntry *LogEntry) *LogEntry {
	if baseEntry == nil {
		return nil
	}

	// Create a copy of the entry for the stream.
	// The stream observer will complete and write it when the stream closes.
	entryCopy := &LogEntry{
		ID:                     baseEntry.ID,
		Timestamp:              baseEntry.Timestamp,
		DurationNs:             baseEntry.DurationNs,
		Model:                  baseEntry.Model,
		ResolvedModel:          baseEntry.ResolvedModel,
		Provider:               baseEntry.Provider,
		AliasUsed:              baseEntry.AliasUsed,
		ExecutionPlanVersionID: baseEntry.ExecutionPlanVersionID,
		CacheType:              baseEntry.CacheType,
		StatusCode:             baseEntry.StatusCode,
		// Copy extracted fields
		RequestID:  baseEntry.RequestID,
		AuthKeyID:  baseEntry.AuthKeyID,
		AuthMethod: baseEntry.AuthMethod,
		ClientIP:   baseEntry.ClientIP,
		Method:     baseEntry.Method,
		Path:       baseEntry.Path,
		UserPath:   baseEntry.UserPath,
		Stream:     true, // Mark as streaming
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
		if baseEntry.Data.ExecutionFeatures != nil {
			snapshot := *baseEntry.Data.ExecutionFeatures
			entryCopy.Data.ExecutionFeatures = &snapshot
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
	maps.Copy(result, m)
	return result
}

// GetStreamEntryFromContext retrieves the log entry from Echo context for streaming.
// This allows handlers to get the entry for wrapping streams.
func GetStreamEntryFromContext(c interface{ Get(string) any }) *LogEntry {
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
// knows not to log it (the stream observer path will handle logging).
func MarkEntryAsStreaming(c interface{ Set(string, any) }, isStreaming bool) {
	c.Set(string(LogEntryStreamingKey), isStreaming)
}

// IsEntryMarkedAsStreaming checks if the entry is marked as streaming.
func IsEntryMarkedAsStreaming(c interface{ Get(string) any }) bool {
	val := c.Get(string(LogEntryStreamingKey))
	if val == nil {
		return false
	}
	streaming, _ := val.(bool)
	return streaming
}
