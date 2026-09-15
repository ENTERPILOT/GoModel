package usage

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingLogger tracks written entries for testing.
type trackingLogger struct {
	entries []*UsageEntry
	mu      sync.Mutex
	enabled bool
}

func (l *trackingLogger) Write(entry *UsageEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *trackingLogger) Config() Config {
	return Config{Enabled: l.enabled}
}

func (l *trackingLogger) Close() error {
	return nil
}

func (l *trackingLogger) getEntries() []*UsageEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]*UsageEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

type streamPricingCaptureResolver struct {
	model    string
	provider string
	pricing  *core.ModelPricing
}

func (r *streamPricingCaptureResolver) ResolvePricing(model, provider string) *core.ModelPricing {
	r.model = model
	r.provider = provider
	return r.pricing
}

// chatUsageEvent is a minimal chat completion chunk carrying 10/5/15 usage
// tokens, shared by the observer tests that only care about entry metadata.
func chatUsageEvent() map[string]any {
	return map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	}
}

func TestStreamUsageObserverChatCompletionStream(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil),
	)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 10, entry.InputTokens)
	assert.Equal(t, 5, entry.OutputTokens)
	assert.Equal(t, 15, entry.TotalTokens)
	assert.Equal(t, "chatcmpl-123", entry.ProviderID)
	assert.Equal(t, "gpt-4", entry.Model)
}

func TestStreamUsageObserverPricesRequestedModelWhenEventModelIsVersioned(t *testing.T) {
	zero := 0.0
	perRequest := 0.033333
	resolver := &streamPricingCaptureResolver{pricing: &core.ModelPricing{
		InputPerMtok:  &zero,
		OutputPerMtok: &zero,
		PerRequest:    &perRequest,
	}}
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4o-mini", "openai", "req-stream", "/v1/chat/completions", resolver)
	observer.SetProviderName("openai")
	observer.OnJSONEvent(map[string]any{
		"id":    "chatcmpl-stream",
		"model": "gpt-4o-mini-2024-07-18",
		"usage": map[string]any{
			"prompt_tokens":     float64(12),
			"completion_tokens": float64(1),
			"total_tokens":      float64(13),
		},
	})
	observer.OnStreamClose()

	require.Equal(t, "gpt-4o-mini", resolver.model)
	require.Equal(t, "openai", resolver.provider)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.Equal(t, "gpt-4o-mini-2024-07-18", entry.Model)
	require.NotNil(t, entry.TotalCost)
	require.InDelta(t, perRequest, *entry.TotalCost, 0.0000001)
}

func TestStreamUsageObserverWithExtendedUsage(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "o1-preview", "openai", "req-456", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id":    "chatcmpl-456",
		"model": "o1-preview",
		"usage": map[string]any{
			"prompt_tokens":     float64(100),
			"completion_tokens": float64(50),
			"total_tokens":      float64(150),
			"prompt_tokens_details": map[string]any{
				"cached_tokens": float64(20),
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": float64(10),
			},
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 50, entry.OutputTokens)
	require.NotNil(t, entry.RawData)
	assert.Equal(t, 20, entry.RawData["prompt_cached_tokens"])
	assert.Equal(t, 10, entry.RawData["completion_reasoning_tokens"])
}

func TestStreamUsageObserverOpenRouterCreditCost(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "openai/gpt-4o", "openrouter", "req-openrouter", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id":    "gen-openrouter",
		"model": "openai/gpt-4o",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(4),
			"total_tokens":      float64(14),
			"cost":              float64(0.00014),
			"cost_details": map[string]any{
				"upstream_inference_prompt_cost":      float64(0.00010),
				"upstream_inference_completions_cost": float64(0.00004),
			},
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.NotNil(t, entry.RawData)
	require.Equal(t, 0.00014, entry.RawData["cost"])
	require.NotNil(t, entry.TotalCost)
	require.Equal(t, 0.00014, *entry.TotalCost)
	require.NotNil(t, entry.InputCost)
	require.Equal(t, 0.00010, *entry.InputCost)
	require.NotNil(t, entry.OutputCost)
	require.Equal(t, 0.00004, *entry.OutputCost)
	require.Equal(t, CostSourceOpenRouterCredits, entry.CostSource)
}

func TestStreamUsageObserverXAITickCost(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "grok-4.3", "xai", "req-xai", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id":    "chatcmpl-xai",
		"model": "grok-4.3",
		"usage": map[string]any{
			"prompt_tokens":      float64(199),
			"completion_tokens":  float64(1),
			"total_tokens":       float64(200),
			"cost_in_usd_ticks":  float64(158_500),
			"num_sources_used":   float64(2),
			"server_tool_calls":  float64(1),
			"zero_value_ignored": float64(0),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.NotNil(t, entry.RawData)
	require.Equal(t, 158_500.0, entry.RawData["cost_in_usd_ticks"])
	require.NotNil(t, entry.TotalCost)
	require.InDelta(t, 0.00001585, *entry.TotalCost, 1e-12)
	require.Nil(t, entry.InputCost)
	require.Nil(t, entry.OutputCost)
	require.Equal(t, CostSourceXAITicks, entry.CostSource)
}

func TestStreamUsageObserverNoUsage(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-789","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-4", "openai", "req-789", "/v1/chat/completions", nil),
	)

	_, _ = io.ReadAll(stream)
	_ = stream.Close()

	entries := logger.getEntries()
	assert.Empty(t, entries)
}

func TestStreamUsageObserverIncludesUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "/team/alpha")
	observer.SetSessionID(" scoped-session ")
	observer.OnJSONEvent(chatUsageEvent())
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)
	require.Equal(t, "/team/alpha", entries[0].UserPath)
	require.Equal(t, "scoped-session", entries[0].SessionID)
}

func TestStreamUsageObserverNoUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil)
	observer.OnJSONEvent(chatUsageEvent())
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)
	require.Equal(t, "/", entries[0].UserPath)

	explicitEmptyLogger := &trackingLogger{enabled: true}
	explicitEmptyObserver := NewStreamUsageObserver(explicitEmptyLogger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "")
	explicitEmptyObserver.OnJSONEvent(chatUsageEvent())
	explicitEmptyObserver.OnStreamClose()

	explicitEmptyEntries := explicitEmptyLogger.getEntries()
	require.Len(t, explicitEmptyEntries, 1)
	require.Equal(t, "/", explicitEmptyEntries[0].UserPath)
}

func TestStreamUsageObserverNormalizesUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, " team//alpha/ ")
	observer.OnJSONEvent(chatUsageEvent())
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)
	require.Equal(t, "/team/alpha", entries[0].UserPath)
}

func TestStreamUsageObserverFallsBackToRootForInvalidUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "/team/../alpha")
	observer.OnJSONEvent(chatUsageEvent())
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)
	require.Equal(t, "/", entries[0].UserPath)
}

func TestStreamUsageObserverDoubleClose(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil)
	observer.OnJSONEvent(chatUsageEvent())

	observer.OnStreamClose()
	observer.OnStreamClose()

	entries := logger.getEntries()
	assert.Len(t, entries, 1)
}

func TestStreamUsageObserverResponsesAPI(t *testing.T) {
	streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp-123","object":"response","status":"in_progress","model":"gpt-5"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" world!"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp-123","object":"response","status":"completed","model":"gpt-5","output":[{"id":"msg_001","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world!"}]}],"usage":{"input_tokens":15,"output_tokens":8,"total_tokens":23}}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-5", "openai", "req-resp-1", "/v1/responses", nil),
	)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 15, entry.InputTokens)
	assert.Equal(t, 8, entry.OutputTokens)
	assert.Equal(t, 23, entry.TotalTokens)
	assert.Equal(t, "resp-123", entry.ProviderID)
	assert.Equal(t, "gpt-5", entry.Model)
}

// TestStreamUsageObserverResponsesAPIIncomplete verifies usage is still logged
// when an interrupted stream ends with response.incomplete — the tokens were
// consumed even though the response did not finish.
func TestStreamUsageObserverResponsesAPIIncomplete(t *testing.T) {
	streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp-123","object":"response","status":"in_progress","model":"gpt-5"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.incomplete
data: {"type":"response.incomplete","response":{"id":"resp-123","object":"response","status":"incomplete","model":"gpt-5","incomplete_details":{"reason":"interrupted"},"output":[],"usage":{"input_tokens":15,"output_tokens":2,"total_tokens":17}}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-5", "openai", "req-resp-2", "/v1/responses", nil),
	)
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 15, entry.InputTokens)
	assert.Equal(t, 2, entry.OutputTokens)
	assert.Equal(t, 17, entry.TotalTokens)
	assert.Equal(t, "resp-123", entry.ProviderID)
}

func TestStreamUsageObserverResponsesAPIWithDetailedUsage(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-5", "openai", "req-resp-detailed", "/v1/responses", nil)
	observer.OnJSONEvent(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":    "resp-456",
			"model": "gpt-5",
			"usage": map[string]any{
				"input_tokens":  float64(125),
				"output_tokens": float64(48),
				"total_tokens":  float64(173),
				"input_tokens_details": map[string]any{
					"cached_tokens": float64(98),
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": float64(7),
				},
				"cost_in_usd_ticks": float64(158500),
			},
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.NotNil(t, entry.RawData)
	require.Equal(t, 98, entry.RawData["prompt_cached_tokens"])
	require.Equal(t, 7, entry.RawData["completion_reasoning_tokens"])
	got, ok := numericFloat(entry.RawData["cost_in_usd_ticks"])
	require.True(t, ok)
	require.Equal(t, float64(158500), got)
}

func TestStreamUsageObserverAnthropicCacheFields(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "claude-sonnet-4-5", "anthropic", "req-anthropic", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id": "msg-123",
		"usage": map[string]any{
			"prompt_tokens":               float64(10),
			"completion_tokens":           float64(2),
			"total_tokens":                float64(12),
			"cache_read_input_tokens":     float64(6),
			"cache_creation_input_tokens": float64(4),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.NotNil(t, entry.RawData)
	require.Equal(t, 6, entry.RawData["cache_read_input_tokens"])
	require.Equal(t, 4, entry.RawData["cache_creation_input_tokens"])
}

func TestStreamUsageObserverLargeResponsesDone(t *testing.T) {
	largeText := strings.Repeat("This is a long response from the model. ", 300)
	streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp-large","object":"response","status":"in_progress","model":"gpt-5"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"start"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp-large","object":"response","status":"completed","model":"gpt-5","output":[{"id":"msg_001","type":"message","role":"assistant","content":[{"type":"output_text","text":"` + largeText + `"}]}],"usage":{"input_tokens":100,"output_tokens":500,"total_tokens":600}}}

data: [DONE]

`
	doneEventStart := strings.Index(streamData, `data: {"type":"response.completed"`)
	doneEventEnd := strings.Index(streamData[doneEventStart:], "\n\n")
	doneEventSize := doneEventEnd
	require.Greater(t, doneEventSize, 8192)

	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-5", "openai", "req-large", "/v1/responses", nil),
	)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, streamData, string(data))
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 500, entry.OutputTokens)
	assert.Equal(t, 600, entry.TotalTokens)
	assert.Equal(t, "resp-large", entry.ProviderID)
}

func TestStreamUsageObserverSmallReads(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-frag","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-4", "openai", "req-frag", "/v1/chat/completions", nil),
	)

	buf := make([]byte, 7)
	var allData []byte
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			allData = append(allData, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, streamData, string(allData))
	err := stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 5, entry.InputTokens)
	assert.Equal(t, 3, entry.OutputTokens)
	assert.Equal(t, 8, entry.TotalTokens)
}

func TestStreamUsageObserverRecordsRewriteSavings(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-1","model":"gpt-4","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	resolver := &streamPricingCaptureResolver{pricing: &core.ModelPricing{
		InputPerMtok:  new(2.0),
		OutputPerMtok: new(8.0),
	}}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-1", "/v1/chat/completions", resolver)
	observer.SetRewriteTokensSaved(500_000)

	stream := streaming.NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 500_000, entry.RewriteTokensSaved)
	require.NotNil(t, entry.RewriteCostSaved)

	assert.InDelta(t, 1.0, *entry.RewriteCostSaved, 1e-9, "500k tokens at $2/Mtok")
}

func TestStreamUsageObserverRewriteSavingsWithoutPricing(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-1","model":"local","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "local", "ollama", "req-1", "/v1/chat/completions", nil)
	observer.SetRewriteTokensSaved(40)

	stream := streaming.NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)

	entries := logger.getEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, 40, entries[0].RewriteTokensSaved)
	assert.Nil(t, entries[0].RewriteCostSaved)
}

func TestStreamUsageObserverAnthropicNativeEvents(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "claude-fable-5", "anthropic", "req-native", "/v1/messages", nil)

	// Anthropic-native streams split usage across events: input tokens and
	// cache details arrive in message_start, output tokens in the final
	// message_delta.
	observer.OnJSONEvent(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    "msg_native",
			"model": "claude-fable-5",
			"usage": map[string]any{
				"input_tokens":                float64(19560),
				"output_tokens":               float64(3),
				"cache_creation_input_tokens": float64(100),
				"cache_read_input_tokens":     float64(200),
			},
		},
	})
	observer.OnJSONEvent(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": float64(31)},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, 19560, entry.InputTokens)
	assert.Equal(t, 31, entry.OutputTokens)
	assert.Equal(t, 19591, entry.TotalTokens)
	assert.Equal(t, "msg_native", entry.ProviderID)
	assert.Equal(t, 100, entry.RawData["cache_creation_input_tokens"])
	assert.Equal(t, 200, entry.RawData["cache_read_input_tokens"])
}
