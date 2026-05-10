package auditlog

import (
	"maps"
	"sort"
	"strings"
)

// Note: MaxContentCapture and LogEntryStreamingKey constants are defined in constants.go

// streamResponseBuilder accumulates data from SSE events to reconstruct a response
type streamResponseBuilder struct {
	// ChatCompletion fields
	ID                string
	Model             string
	Provider          string
	SystemFingerprint string
	Created           int64
	Usage             map[string]any
	Choices           map[int]*streamChatChoiceState

	// Responses API fields
	IsResponsesAPI bool
	ResponseID     string
	CreatedAt      int64
	Status         string
	OutputText     strings.Builder

	// Tracking
	contentLen int // track content length to enforce limit
	truncated  bool
}

type streamChatToolCallState struct {
	ID          string
	Type        string
	Name        string
	Arguments   strings.Builder
	hasFunction bool
}

type streamChatChoiceState struct {
	Role         string
	Content      strings.Builder
	FinishReason string
	ToolCalls    map[int]*streamChatToolCallState
}

// buildChatCompletionResponse constructs a ChatCompletion response from accumulated data
func (b *streamResponseBuilder) buildChatCompletionResponse() map[string]any {
	choices := b.buildChatChoices()

	response := map[string]any{
		"id":      b.ID,
		"object":  "chat.completion",
		"model":   b.Model,
		"created": b.Created,
		"choices": choices,
	}
	if b.Provider != "" {
		response["provider"] = b.Provider
	}
	if b.SystemFingerprint != "" {
		response["system_fingerprint"] = b.SystemFingerprint
	}
	if b.Usage != nil {
		response["usage"] = b.Usage
	}
	return response
}

func (b *streamResponseBuilder) buildChatChoices() []map[string]any {
	if len(b.Choices) == 0 {
		return []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
				},
				"finish_reason": "",
			},
		}
	}

	indexes := make([]int, 0, len(b.Choices))
	for index := range b.Choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	choices := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		state := b.Choices[index]
		message := map[string]any{
			"role": nonEmptyString(state.Role, "assistant"),
		}

		content := state.Content.String()
		toolCalls := buildStreamChatToolCalls(state.ToolCalls)
		// OpenAI chat messages distinguish tool-only output from an explicitly
		// empty message: no state.Content.String() plus state.ToolCalls renders
		// message["content"] as nil; no text and no buildStreamChatToolCalls(...)
		// result renders message["content"] as "".
		switch {
		case content != "":
			message["content"] = content
		case len(toolCalls) > 0:
			message["content"] = nil
		default:
			message["content"] = ""
		}
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
		}

		choices = append(choices, map[string]any{
			"index":         index,
			"message":       message,
			"finish_reason": state.FinishReason,
		})
	}

	return choices
}

func buildStreamChatToolCalls(states map[int]*streamChatToolCallState) []map[string]any {
	if len(states) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(states))
	for index := range states {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	toolCalls := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		state := states[index]
		if !state.hasFunction {
			continue
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   state.ID,
			"type": nonEmptyString(state.Type, "function"),
			"function": map[string]any{
				"name":      state.Name,
				"arguments": state.Arguments.String(),
			},
		})
	}
	return toolCalls
}

func (b *streamResponseBuilder) chatChoice(index int) *streamChatChoiceState {
	if b.Choices == nil {
		b.Choices = make(map[int]*streamChatChoiceState)
	}
	state, ok := b.Choices[index]
	if ok {
		return state
	}
	state = &streamChatChoiceState{ToolCalls: make(map[int]*streamChatToolCallState)}
	b.Choices[index] = state
	return state
}

func (c *streamChatChoiceState) toolCall(index int) *streamChatToolCallState {
	if c.ToolCalls == nil {
		c.ToolCalls = make(map[int]*streamChatToolCallState)
	}
	state, ok := c.ToolCalls[index]
	if ok {
		return state
	}
	state = &streamChatToolCallState{}
	c.ToolCalls[index] = state
	return state
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
						"text": b.OutputText.String(),
					},
				},
			},
		},
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
		ID:                baseEntry.ID,
		Timestamp:         baseEntry.Timestamp,
		DurationNs:        baseEntry.DurationNs,
		RequestedModel:    baseEntry.RequestedModel,
		ResolvedModel:     baseEntry.ResolvedModel,
		Provider:          baseEntry.Provider,
		ProviderName:      baseEntry.ProviderName,
		AliasUsed:         baseEntry.AliasUsed,
		WorkflowVersionID: baseEntry.WorkflowVersionID,
		CacheType:         baseEntry.CacheType,
		StatusCode:        baseEntry.StatusCode,
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
		if baseEntry.Data.WorkflowFeatures != nil {
			snapshot := *baseEntry.Data.WorkflowFeatures
			entryCopy.Data.WorkflowFeatures = &snapshot
		}
		if baseEntry.Data.Failover != nil {
			snapshot := *baseEntry.Data.Failover
			entryCopy.Data.Failover = &snapshot
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

func copyAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

func jsonNumberToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func jsonNumberToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

func nonEmptyString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
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
