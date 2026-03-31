package usage

import (
	"io"
	"strings"
	"sync"
	"testing"

	"gomodel/internal/streaming"
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
