package usage

import (
	"io"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
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
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", entry.InputTokens)
	}
	if entry.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", entry.OutputTokens)
	}
	if entry.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", entry.TotalTokens)
	}
	if entry.ProviderID != "chatcmpl-123" {
		t.Errorf("ProviderID = %s, want chatcmpl-123", entry.ProviderID)
	}
	if entry.Model != "gpt-4" {
		t.Errorf("Model = %s, want gpt-4", entry.Model)
	}
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

	if resolver.model != "gpt-4o-mini" {
		t.Fatalf("pricing model = %q, want gpt-4o-mini", resolver.model)
	}
	if resolver.provider != "openai" {
		t.Fatalf("pricing provider = %q, want openai", resolver.provider)
	}
	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Model != "gpt-4o-mini-2024-07-18" {
		t.Fatalf("usage model = %q, want provider event model", entry.Model)
	}
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-perRequest) > 0.0000001 {
		t.Fatalf("total cost = %v, want %f", entry.TotalCost, perRequest)
	}
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", entry.OutputTokens)
	}
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set")
	}
	if entry.RawData["prompt_cached_tokens"] != 20 {
		t.Errorf("RawData[prompt_cached_tokens] = %v, want 20", entry.RawData["prompt_cached_tokens"])
	}
	if entry.RawData["completion_reasoning_tokens"] != 10 {
		t.Errorf("RawData[completion_reasoning_tokens] = %v, want 10", entry.RawData["completion_reasoning_tokens"])
	}
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.RawData == nil || entry.RawData["cost"] != 0.00014 {
		t.Fatalf("RawData[cost] = %#v, want 0.00014", entry.RawData["cost"])
	}
	if entry.TotalCost == nil || *entry.TotalCost != 0.00014 {
		t.Fatalf("TotalCost = %v, want 0.00014", entry.TotalCost)
	}
	if entry.InputCost == nil || *entry.InputCost != 0.00010 {
		t.Fatalf("InputCost = %v, want 0.00010", entry.InputCost)
	}
	if entry.OutputCost == nil || *entry.OutputCost != 0.00004 {
		t.Fatalf("OutputCost = %v, want 0.00004", entry.OutputCost)
	}
	if entry.CostSource != CostSourceOpenRouterCredits {
		t.Fatalf("CostSource = %q, want %q", entry.CostSource, CostSourceOpenRouterCredits)
	}
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.RawData == nil || entry.RawData["cost_in_usd_ticks"] != 158_500.0 {
		t.Fatalf("RawData[cost_in_usd_ticks] = %#v, want 158500", entry.RawData["cost_in_usd_ticks"])
	}
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-0.00001585) > 1e-12 {
		t.Fatalf("TotalCost = %v, want 0.00001585", entry.TotalCost)
	}
	if entry.InputCost != nil || entry.OutputCost != nil {
		t.Fatalf("InputCost/OutputCost = %v/%v, want nil without response split", entry.InputCost, entry.OutputCost)
	}
	if entry.CostSource != CostSourceXAITicks {
		t.Fatalf("CostSource = %q, want %q", entry.CostSource, CostSourceXAITicks)
	}
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
	if len(entries) != 0 {
		t.Errorf("expected 0 entries (no usage), got %d", len(entries))
	}
}

func TestStreamUsageObserverIncludesUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "/team/alpha")
	observer.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if got := entries[0].UserPath; got != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", got)
	}
}

func TestStreamUsageObserverNoUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if got := entries[0].UserPath; got != "/" {
		t.Fatalf("UserPath = %q, want /", got)
	}

	explicitEmptyLogger := &trackingLogger{enabled: true}
	explicitEmptyObserver := NewStreamUsageObserver(explicitEmptyLogger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "")
	explicitEmptyObserver.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})
	explicitEmptyObserver.OnStreamClose()

	explicitEmptyEntries := explicitEmptyLogger.getEntries()
	if len(explicitEmptyEntries) != 1 {
		t.Fatalf("explicit empty len(entries) = %d, want 1", len(explicitEmptyEntries))
	}
	if got := explicitEmptyEntries[0].UserPath; got != "/" {
		t.Fatalf("explicit empty UserPath = %q, want /", got)
	}
}

func TestStreamUsageObserverNormalizesUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, " team//alpha/ ")
	observer.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if got := entries[0].UserPath; got != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", got)
	}
}

func TestStreamUsageObserverFallsBackToRootForInvalidUserPath(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil, "/team/../alpha")
	observer.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})
	observer.OnStreamClose()

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if got := entries[0].UserPath; got != "/" {
		t.Fatalf("UserPath = %q, want /", got)
	}
}

func TestStreamUsageObserverDoubleClose(t *testing.T) {
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "gpt-4", "openai", "req-123", "/v1/chat/completions", nil)
	observer.OnJSONEvent(map[string]any{
		"id": "chatcmpl-123",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	})

	observer.OnStreamClose()
	observer.OnStreamClose()

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (not 2 from double close), got %d", len(entries))
	}
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
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Errorf("data mismatch: got %d bytes, want %d bytes", len(data), len(streamData))
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", entry.InputTokens)
	}
	if entry.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8", entry.OutputTokens)
	}
	if entry.TotalTokens != 23 {
		t.Errorf("TotalTokens = %d, want 23", entry.TotalTokens)
	}
	if entry.ProviderID != "resp-123" {
		t.Errorf("ProviderID = %s, want resp-123", entry.ProviderID)
	}
	if entry.Model != "gpt-5" {
		t.Errorf("Model = %s, want gpt-5", entry.Model)
	}
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set")
	}
	if entry.RawData["prompt_cached_tokens"] != 98 {
		t.Fatalf("RawData[prompt_cached_tokens] = %v, want 98", entry.RawData["prompt_cached_tokens"])
	}
	if entry.RawData["completion_reasoning_tokens"] != 7 {
		t.Fatalf("RawData[completion_reasoning_tokens] = %v, want 7", entry.RawData["completion_reasoning_tokens"])
	}
	if got, ok := numericFloat(entry.RawData["cost_in_usd_ticks"]); !ok || got != 158500 {
		t.Fatalf("RawData[cost_in_usd_ticks] = %v, want 158500", entry.RawData["cost_in_usd_ticks"])
	}
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.RawData == nil {
		t.Fatal("expected RawData to be set")
	}
	if entry.RawData["cache_read_input_tokens"] != 6 {
		t.Fatalf("RawData[cache_read_input_tokens] = %v, want 6", entry.RawData["cache_read_input_tokens"])
	}
	if entry.RawData["cache_creation_input_tokens"] != 4 {
		t.Fatalf("RawData[cache_creation_input_tokens] = %v, want 4", entry.RawData["cache_creation_input_tokens"])
	}
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
	if doneEventSize <= 8192 {
		t.Fatalf("test setup error: response.completed event is only %d bytes, need >8192", doneEventSize)
	}

	logger := &trackingLogger{enabled: true}
	stream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamData)),
		NewStreamUsageObserver(logger, "gpt-5", "openai", "req-large", "/v1/responses", nil),
	)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Errorf("data mismatch: got %d bytes, want %d bytes", len(data), len(streamData))
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d (usage was lost from large response.completed event)", len(entries))
	}
	entry := entries[0]
	if entry.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", entry.OutputTokens)
	}
	if entry.TotalTokens != 600 {
		t.Errorf("TotalTokens = %d, want 600", entry.TotalTokens)
	}
	if entry.ProviderID != "resp-large" {
		t.Errorf("ProviderID = %s, want resp-large", entry.ProviderID)
	}
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
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}

	if string(allData) != streamData {
		t.Errorf("data mismatch: got %d bytes, want %d bytes", len(allData), len(streamData))
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", entry.InputTokens)
	}
	if entry.OutputTokens != 3 {
		t.Errorf("OutputTokens = %d, want 3", entry.OutputTokens)
	}
	if entry.TotalTokens != 8 {
		t.Errorf("TotalTokens = %d, want 8", entry.TotalTokens)
	}
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
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.RewriteTokensSaved != 500_000 {
		t.Errorf("RewriteTokensSaved = %d, want 500000", entry.RewriteTokensSaved)
	}
	if entry.RewriteCostSaved == nil {
		t.Fatal("expected RewriteCostSaved with resolvable pricing")
	}
	if got, want := *entry.RewriteCostSaved, 1.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("RewriteCostSaved = %v, want %v (500k tokens at $2/Mtok)", got, want)
	}
}

func TestStreamUsageObserverRewriteSavingsWithoutPricing(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-1","model":"local","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}

data: [DONE]

`
	logger := &trackingLogger{enabled: true}
	observer := NewStreamUsageObserver(logger, "local", "ollama", "req-1", "/v1/chat/completions", nil)
	observer.SetRewriteTokensSaved(40)

	stream := streaming.NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	entries := logger.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].RewriteTokensSaved != 40 {
		t.Errorf("RewriteTokensSaved = %d, want 40", entries[0].RewriteTokensSaved)
	}
	if entries[0].RewriteCostSaved != nil {
		t.Errorf("RewriteCostSaved = %v, want nil without pricing", *entries[0].RewriteCostSaved)
	}
}
