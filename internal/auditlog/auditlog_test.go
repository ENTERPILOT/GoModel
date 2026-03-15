package auditlog

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func TestRedactHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name:     "nil headers",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty headers",
			input:    map[string]string{},
			expected: map[string]string{},
		},
		{
			name: "no sensitive headers",
			input: map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json",
			},
			expected: map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json",
			},
		},
		{
			name: "redact authorization",
			input: map[string]string{
				"Authorization": "Bearer sk-secret-key",
				"Content-Type":  "application/json",
			},
			expected: map[string]string{
				"Authorization": "[REDACTED]",
				"Content-Type":  "application/json",
			},
		},
		{
			name: "redact multiple sensitive headers",
			input: map[string]string{
				"Authorization":       "Bearer token",
				"X-Api-Key":           "secret-key",
				"Cookie":              "session=abc123",
				"Content-Type":        "application/json",
				"X-Auth-Token":        "some-token",
				"Proxy-Authorization": "Basic creds",
			},
			expected: map[string]string{
				"Authorization":       "[REDACTED]",
				"X-Api-Key":           "[REDACTED]",
				"Cookie":              "[REDACTED]",
				"Content-Type":        "application/json",
				"X-Auth-Token":        "[REDACTED]",
				"Proxy-Authorization": "[REDACTED]",
			},
		},
		{
			name: "case insensitive redaction",
			input: map[string]string{
				"AUTHORIZATION": "Bearer token",
				"x-api-key":     "secret",
				"X-API-KEY":     "another-secret",
			},
			expected: map[string]string{
				"AUTHORIZATION": "[REDACTED]",
				"x-api-key":     "[REDACTED]",
				"X-API-KEY":     "[REDACTED]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactHeaders(tt.input)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d headers, got %d", len(tt.expected), len(result))
			}

			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("header %q: expected %q, got %q", k, v, result[k])
				}
			}
		})
	}
}

func TestLogEntryJSON(t *testing.T) {
	entry := &LogEntry{
		ID:            "test-id-123",
		Timestamp:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		DurationNs:    1500000,
		Model:         "friendly-alias",
		ResolvedModel: "openai/gpt-4",
		Provider:      "openai",
		AliasUsed:     true,
		StatusCode:    200,
		RequestID:     "req-123",
		ClientIP:      "192.168.1.1",
		Method:        "POST",
		Path:          "/v1/chat/completions",
		Stream:        false,
		Data: &LogData{
			UserAgent: "test-agent",
		},
	}

	// Test JSON marshaling
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal entry: %v", err)
	}

	// Test JSON unmarshaling
	var decoded LogEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal entry: %v", err)
	}

	// Verify fields
	if decoded.ID != entry.ID {
		t.Errorf("ID mismatch: expected %q, got %q", entry.ID, decoded.ID)
	}
	if decoded.Model != entry.Model {
		t.Errorf("Model mismatch: expected %q, got %q", entry.Model, decoded.Model)
	}
	if decoded.Provider != entry.Provider {
		t.Errorf("Provider mismatch: expected %q, got %q", entry.Provider, decoded.Provider)
	}
	if decoded.ResolvedModel != entry.ResolvedModel {
		t.Errorf("ResolvedModel mismatch: expected %q, got %q", entry.ResolvedModel, decoded.ResolvedModel)
	}
	if decoded.AliasUsed != entry.AliasUsed {
		t.Errorf("AliasUsed mismatch: expected %v, got %v", entry.AliasUsed, decoded.AliasUsed)
	}
	if decoded.StatusCode != entry.StatusCode {
		t.Errorf("StatusCode mismatch: expected %d, got %d", entry.StatusCode, decoded.StatusCode)
	}
	if decoded.RequestID != entry.RequestID {
		t.Errorf("RequestID mismatch: expected %q, got %q", entry.RequestID, decoded.RequestID)
	}
}

func TestLogDataWithBodies(t *testing.T) {
	// Use interface{} types (maps) for bodies - this is how they're stored now
	requestBody := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []interface{}{},
	}
	responseBody := map[string]interface{}{
		"id":      "resp-123",
		"choices": []interface{}{},
	}

	data := &LogData{
		UserAgent:    "test-agent",
		RequestBody:  requestBody,
		ResponseBody: responseBody,
	}

	// Marshal and unmarshal
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded LogData
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify bodies are preserved (decoded as map[string]interface{})
	decodedReqBody, ok := decoded.RequestBody.(map[string]interface{})
	if !ok {
		t.Fatalf("RequestBody is not a map, got %T", decoded.RequestBody)
	}
	if decodedReqBody["model"] != "gpt-4" {
		t.Errorf("RequestBody model mismatch: expected gpt-4, got %v", decodedReqBody["model"])
	}

	decodedRespBody, ok := decoded.ResponseBody.(map[string]interface{})
	if !ok {
		t.Fatalf("ResponseBody is not a map, got %T", decoded.ResponseBody)
	}
	if decodedRespBody["id"] != "resp-123" {
		t.Errorf("ResponseBody id mismatch: expected resp-123, got %v", decodedRespBody["id"])
	}
}

// mockStore implements LogStore for testing
type mockStore struct {
	mu      sync.Mutex
	entries []*LogEntry
	closed  bool
}

type capturingLogger struct {
	cfg     Config
	entries []*LogEntry
}

func (l *capturingLogger) Write(entry *LogEntry) {
	l.entries = append(l.entries, entry)
}

func (l *capturingLogger) Config() Config {
	return l.cfg
}

func (l *capturingLogger) Close() error {
	return nil
}

type readCountCloser struct {
	reader    io.Reader
	readCalls int
}

func (r *readCountCloser) Read(p []byte) (int, error) {
	r.readCalls++
	if r.reader == nil {
		return 0, io.EOF
	}
	return r.reader.Read(p)
}

func (r *readCountCloser) Close() error {
	return nil
}

func (m *mockStore) WriteBatch(_ context.Context, entries []*LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	return nil
}

func (m *mockStore) Flush(_ context.Context) error {
	return nil
}

func (m *mockStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockStore) getEntries() []*LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries
}

func (m *mockStore) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func TestLogger(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    10,
		FlushInterval: 100 * time.Millisecond,
	}

	logger := NewLogger(store, cfg)
	defer logger.Close()

	// Write some entries
	for i := 0; i < 5; i++ {
		logger.Write(&LogEntry{
			ID:        fmt.Sprintf("entry-%d", i),
			Timestamp: time.Now(),
			Model:     "test-model",
		})
	}

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	// Verify entries were written
	if len(store.getEntries()) != 5 {
		t.Errorf("expected 5 entries, got %d", len(store.getEntries()))
	}
}

func TestMiddleware_UsesIngressFrameRequestBodyWithoutReadingStream(t *testing.T) {
	e := echo.New()
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(`{"model":"from-body"}`)}
	req.Body = trackedBody
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"",
		[]byte(`{"model":"from-ingress"}`),
		false,
		"",
		nil,
	)))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if trackedBody.readCalls != 0 {
		t.Fatalf("request body was read %d times, want 0", trackedBody.readCalls)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}

	requestBody, ok := logger.entries[0].Data.RequestBody.(map[string]any)
	if !ok {
		t.Fatalf("RequestBody = %T, want map[string]any", logger.entries[0].Data.RequestBody)
	}
	if requestBody["model"] != "from-ingress" {
		t.Fatalf("RequestBody.model = %#v, want from-ingress", requestBody["model"])
	}
}

func TestMiddleware_UsesIngressTooLargeFlagWithoutReadingStream(t *testing.T) {
	e := echo.New()
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(strings.Repeat("x", 16))}
	req.Body = trackedBody
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"",
		nil,
		true,
		"",
		nil,
	)))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if trackedBody.readCalls != 0 {
		t.Fatalf("request body was read %d times, want 0", trackedBody.readCalls)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	if !logger.entries[0].Data.RequestBodyTooBigToHandle {
		t.Fatal("RequestBodyTooBigToHandle = false, want true")
	}
	if logger.entries[0].Data.RequestBody != nil {
		t.Fatalf("RequestBody = %#v, want nil", logger.entries[0].Data.RequestBody)
	}
}

func TestMiddleware_PrefersExecutionPlanOverLegacyResolution(t *testing.T) {
	e := echo.New()
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"anthropic/claude-opus-4-6"}`))
	req = req.WithContext(core.WithExecutionPlan(req.Context(), &core.ExecutionPlan{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			RequestedModel:   "anthropic/claude-opus-4-6",
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
			ProviderType:     "openai",
			AliasApplied:     true,
		},
	}))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntry(c, "placeholder", "placeholder")
		return c.NoContent(http.StatusNoContent)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}

	entry := logger.entries[0]
	if entry.Model != "anthropic/claude-opus-4-6" {
		t.Fatalf("Model = %q, want requested alias", entry.Model)
	}
	if entry.ResolvedModel != "openai/gpt-5-nano" {
		t.Fatalf("ResolvedModel = %q, want openai/gpt-5-nano", entry.ResolvedModel)
	}
	if entry.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", entry.Provider)
	}
	if !entry.AliasUsed {
		t.Fatal("AliasUsed = false, want true")
	}
}

func TestMiddleware_DoesNotApplyModelMetadataWithoutExecutionPlan(t *testing.T) {
	e := echo.New()
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"legacy-only"}`))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}

	entry := logger.entries[0]
	if entry.Model != "" {
		t.Fatalf("Model = %q, want empty", entry.Model)
	}
	if entry.ResolvedModel != "" {
		t.Fatalf("ResolvedModel = %q, want empty", entry.ResolvedModel)
	}
	if entry.Provider != "" {
		t.Fatalf("Provider = %q, want empty", entry.Provider)
	}
	if entry.AliasUsed {
		t.Fatal("AliasUsed = true, want false")
	}
}

func TestLoggerClose(t *testing.T) {
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    100,
		FlushInterval: 10 * time.Second, // Long interval to test close flushes
	}

	logger := NewLogger(store, cfg)

	// Write entry
	logger.Write(&LogEntry{
		ID:        "test-entry",
		Timestamp: time.Now(),
	})

	// Close should flush
	logger.Close()

	// Verify entry was flushed
	if len(store.getEntries()) != 1 {
		t.Errorf("expected 1 entry after close, got %d", len(store.getEntries()))
	}

	// Verify store was closed
	if !store.isClosed() {
		t.Error("store was not closed")
	}
}

func TestNoopLogger(t *testing.T) {
	logger := &NoopLogger{}

	// Should not panic
	logger.Write(&LogEntry{ID: "test"})
	logger.Close()

	cfg := logger.Config()
	if cfg.Enabled {
		t.Error("noop logger should report as disabled")
	}
}

func TestIsModelInteractionPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"chat completions", "/v1/chat/completions", true},
		{"chat completions with query", "/v1/chat/completions?stream=true", true},
		{"responses", "/v1/responses", true},
		{"responses with subpath", "/v1/responses/123", true},
		{"files", "/v1/files", true},
		{"files with subpath", "/v1/files/file-123", true},
		{"files prefix overmatch", "/v1/fileship", false},
		{"batches", "/v1/batches", true},
		{"batches with subpath", "/v1/batches/123", true},
		{"batches prefix overmatch", "/v1/batcheship", false},
		{"models", "/v1/models", false},
		{"models with subpath", "/v1/models/gpt-4", false},
		{"health", "/health", false},
		{"metrics", "/metrics", false},
		{"admin", "/admin", false},
		{"root", "/", false},
		{"empty", "", false},
		{"v1 prefix only", "/v1", false},
		{"v1 other endpoint", "/v1/other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsModelInteractionPath(tt.path)
			if result != tt.expected {
				t.Errorf("IsModelInteractionPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestStreamLogWrapper(t *testing.T) {
	// Create a mock stream with content
	streamContent := `data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-123","choices":[]}

data: [DONE]

`
	stream := io.NopCloser(strings.NewReader(streamContent))

	// Create mock logger and entry
	store := &mockStore{}
	cfg := Config{
		Enabled:       true,
		BufferSize:    10,
		FlushInterval: 100 * time.Millisecond,
	}
	logger := NewLogger(store, cfg)
	defer logger.Close()

	entry := &LogEntry{
		ID:        "test-entry",
		Timestamp: time.Now(),
		Model:     "gpt-4",
		Data:      &LogData{},
	}

	// Wrap the stream
	wrapper := NewStreamLogWrapper(stream, logger, entry, "/v1/chat/completions")

	// Read all content
	var buf bytes.Buffer
	_, err := io.Copy(&buf, wrapper)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}

	// Close wrapper to trigger logging
	if err := wrapper.Close(); err != nil {
		t.Fatalf("failed to close wrapper: %v", err)
	}

	// Wait for async write
	time.Sleep(200 * time.Millisecond)

	// Verify entry was logged
	if len(store.getEntries()) != 1 {
		t.Errorf("expected 1 entry, got %d", len(store.getEntries()))
	}
}

func TestWrapStreamForLogging(t *testing.T) {
	stream := io.NopCloser(strings.NewReader("test"))

	// Test with nil logger
	result := WrapStreamForLogging(stream, nil, nil, "/v1/chat/completions")
	if result != stream {
		t.Error("expected original stream with nil logger")
	}

	// Test with disabled logger
	noopLogger := &NoopLogger{}
	result = WrapStreamForLogging(stream, noopLogger, &LogEntry{}, "/v1/chat/completions")
	if result != stream {
		t.Error("expected original stream with disabled logger")
	}
}

func TestCreateStreamEntry(t *testing.T) {
	// Test nil input
	result := CreateStreamEntry(nil)
	if result != nil {
		t.Error("expected nil for nil input")
	}

	// Test with valid entry
	baseEntry := &LogEntry{
		ID:            "test-id",
		Timestamp:     time.Now(),
		DurationNs:    1000,
		Model:         "claude-opus-4-6",
		ResolvedModel: "openai/gpt-5-nano",
		Provider:      "openai",
		AliasUsed:     true,
		StatusCode:    200,
		RequestID:     "req-123",
		ClientIP:      "127.0.0.1",
		Method:        "POST",
		Path:          "/v1/chat/completions",
		Stream:        false,
		Data: &LogData{
			UserAgent: "test",
			RequestHeaders: map[string]string{
				"Content-Type": "application/json",
			},
		},
	}

	streamEntry := CreateStreamEntry(baseEntry)
	if streamEntry == nil {
		t.Fatal("expected non-nil stream entry")
		return
	}

	// Verify fields are copied
	if streamEntry.ID != baseEntry.ID {
		t.Errorf("ID mismatch")
	}
	if streamEntry.Model != baseEntry.Model {
		t.Errorf("Model mismatch")
	}
	if streamEntry.ResolvedModel != baseEntry.ResolvedModel {
		t.Errorf("ResolvedModel mismatch")
	}
	if streamEntry.AliasUsed != baseEntry.AliasUsed {
		t.Errorf("AliasUsed mismatch")
	}
	if !streamEntry.Stream {
		t.Error("Stream should be true")
	}
	if streamEntry.RequestID != baseEntry.RequestID {
		t.Error("RequestID not copied")
	}
	if streamEntry.ClientIP != baseEntry.ClientIP {
		t.Error("ClientIP not copied")
	}
	if streamEntry.Method != baseEntry.Method {
		t.Error("Method not copied")
	}
	if streamEntry.Path != baseEntry.Path {
		t.Error("Path not copied")
	}

	// Verify Data fields are copied
	if streamEntry.Data == nil {
		t.Fatal("Data is nil")
		return
	}

	// Verify headers are copied (not same reference)
	if streamEntry.Data.RequestHeaders == nil {
		t.Fatal("RequestHeaders is nil")
		return
	}
	baseEntry.Data.RequestHeaders["New"] = "value"
	if streamEntry.Data.RequestHeaders["New"] == "value" {
		t.Error("Headers should be a copy, not same reference")
	}
}

func TestHashAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		wantEmpty  bool
	}{
		{
			name:       "empty header",
			authHeader: "",
			wantEmpty:  true,
		},
		{
			name:       "Bearer only",
			authHeader: "Bearer ",
			wantEmpty:  true,
		},
		{
			name:       "valid Bearer token",
			authHeader: "Bearer sk-test-key-123",
			wantEmpty:  false,
		},
		{
			name:       "token without Bearer prefix",
			authHeader: "sk-test-key-123",
			wantEmpty:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hashAPIKey(tt.authHeader)
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
			} else {
				if result == "" {
					t.Error("expected non-empty hash")
				}
				if len(result) != 16 {
					t.Errorf("expected 16 character hash, got %d characters", len(result))
				}
			}
		})
	}

	// Test consistency - same input should produce same hash
	hash1 := hashAPIKey("Bearer test-key")
	hash2 := hashAPIKey("Bearer test-key")
	if hash1 != hash2 {
		t.Error("same input should produce same hash")
	}

	// Test different inputs produce different hashes
	hash3 := hashAPIKey("Bearer different-key")
	if hash1 == hash3 {
		t.Error("different inputs should produce different hashes")
	}
}

// Helper compression functions for tests
func compressGzip(data []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func compressDeflate(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func compressBrotli(data []byte) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func TestDecompressBody(t *testing.T) {
	originalData := []byte(`{"message": "hello world", "count": 42}`)

	tests := []struct {
		name             string
		encoding         string
		compressFunc     func([]byte) []byte
		shouldDecompress bool
	}{
		{
			name:             "no encoding",
			encoding:         "",
			compressFunc:     func(b []byte) []byte { return b },
			shouldDecompress: false,
		},
		{
			name:             "identity encoding",
			encoding:         "identity",
			compressFunc:     func(b []byte) []byte { return b },
			shouldDecompress: false,
		},
		{
			name:             "gzip encoding",
			encoding:         "gzip",
			compressFunc:     compressGzip,
			shouldDecompress: true,
		},
		{
			name:             "deflate encoding",
			encoding:         "deflate",
			compressFunc:     compressDeflate,
			shouldDecompress: true,
		},
		{
			name:             "brotli encoding",
			encoding:         "br",
			compressFunc:     compressBrotli,
			shouldDecompress: true,
		},
		{
			name:             "gzip with extra spaces",
			encoding:         "  gzip  ",
			compressFunc:     compressGzip,
			shouldDecompress: true,
		},
		{
			name:             "multiple encodings (first only)",
			encoding:         "gzip, deflate",
			compressFunc:     compressGzip,
			shouldDecompress: true,
		},
		{
			name:             "unknown encoding",
			encoding:         "unknown",
			compressFunc:     func(b []byte) []byte { return b },
			shouldDecompress: false,
		},
		{
			name:             "uppercase gzip",
			encoding:         "GZIP",
			compressFunc:     compressGzip,
			shouldDecompress: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed := tt.compressFunc(originalData)
			result, decompressed := decompressBody(compressed, tt.encoding)

			if decompressed != tt.shouldDecompress {
				t.Errorf("decompressed = %v, want %v", decompressed, tt.shouldDecompress)
			}

			if tt.shouldDecompress {
				if !bytes.Equal(result, originalData) {
					t.Errorf("decompressed data mismatch: got %s, want %s", result, originalData)
				}
			}
		})
	}
}

func TestDecompressBodyInvalidData(t *testing.T) {
	// Invalid compressed data should return original
	invalidData := []byte("not valid compressed data")

	result, decompressed := decompressBody(invalidData, "gzip")
	if decompressed {
		t.Error("expected decompression to fail for invalid gzip data")
	}
	if !bytes.Equal(result, invalidData) {
		t.Error("expected original data to be returned on failure")
	}
}

func TestResponseBodyCapture_Write_SingleLargeChunk(t *testing.T) {
	// A single Write call larger than MaxBodyCapture should be capped
	capture := &responseBodyCapture{
		ResponseWriter: &discardWriter{},
		body:           &bytes.Buffer{},
	}

	// Write a chunk larger than MaxBodyCapture in one call
	largeData := bytes.Repeat([]byte("x"), int(MaxBodyCapture)+1024)
	n, err := capture.Write(largeData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(largeData) {
		t.Errorf("expected %d bytes written to underlying writer, got %d", len(largeData), n)
	}

	// Buffer should be capped at exactly MaxBodyCapture
	if capture.body.Len() != int(MaxBodyCapture) {
		t.Errorf("expected buffer size %d, got %d", MaxBodyCapture, capture.body.Len())
	}
	if !capture.truncated {
		t.Error("expected truncated flag to be set")
	}
}

func TestResponseBodyCapture_Write_MultipleChunksOverflow(t *testing.T) {
	capture := &responseBodyCapture{
		ResponseWriter: &discardWriter{},
		body:           &bytes.Buffer{},
	}

	// Write chunks that collectively exceed MaxBodyCapture
	chunkSize := int(MaxBodyCapture) / 2
	chunk := bytes.Repeat([]byte("a"), chunkSize)

	// First chunk: should fit entirely
	_, _ = capture.Write(chunk)
	if capture.truncated {
		t.Error("should not be truncated after first chunk")
	}
	if capture.body.Len() != chunkSize {
		t.Errorf("expected buffer size %d, got %d", chunkSize, capture.body.Len())
	}

	// Second chunk: fits exactly (no data lost, so truncated remains false)
	_, _ = capture.Write(chunk)
	if capture.truncated {
		t.Error("should not be truncated when buffer is exactly at limit")
	}
	if capture.body.Len() != int(MaxBodyCapture) {
		t.Errorf("expected buffer at %d, got %d", MaxBodyCapture, capture.body.Len())
	}

	// Third chunk: entirely skipped, truncated flag set
	_, _ = capture.Write(chunk)
	if !capture.truncated {
		t.Error("should be truncated after third chunk is rejected")
	}
	if capture.body.Len() != int(MaxBodyCapture) {
		t.Errorf("expected buffer still at %d after third chunk, got %d", MaxBodyCapture, capture.body.Len())
	}
}

// trackingReadCloser wraps an io.Reader and tracks whether Close was called.
type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

// discardWriter implements http.ResponseWriter but discards all output.
type discardWriter struct{}

func (d *discardWriter) Header() http.Header         { return http.Header{} }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

func TestLimitedReaderRequestBodyCapture(t *testing.T) {
	t.Run("chunked request body under limit is captured", func(t *testing.T) {
		body := `{"model":"gpt-4","messages":[]}`
		req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.ContentLength = -1 // Simulate chunked encoding

		entry := &LogEntry{Data: &LogData{}}
		// Simulate the middleware body capture logic
		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}

		if int64(len(bodyBytes)) > MaxBodyCapture {
			t.Fatal("body should be under limit")
		}

		var parsed interface{}
		if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
			entry.Data.RequestBody = parsed
		}

		if entry.Data.RequestBody == nil {
			t.Error("expected request body to be captured for chunked request")
		}
		if entry.Data.RequestBodyTooBigToHandle {
			t.Error("should not be marked as too big")
		}
	})

	t.Run("chunked request body over limit sets flag and preserves downstream body", func(t *testing.T) {
		// Create a body larger than MaxBodyCapture
		largeBody := strings.Repeat("x", int(MaxBodyCapture)+100)
		req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(largeBody))
		req.ContentLength = -1 // Simulate chunked encoding

		entry := &LogEntry{Data: &LogData{}}

		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}

		if int64(len(bodyBytes)) <= MaxBodyCapture {
			t.Fatal("body should exceed limit")
		}

		entry.Data.RequestBodyTooBigToHandle = true
		// Reconstruct body for downstream
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), req.Body))

		// Verify downstream can read the full body
		downstream, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("downstream read error: %v", err)
		}
		if len(downstream) != len(largeBody) {
			t.Errorf("downstream body length mismatch: expected %d, got %d", len(largeBody), len(downstream))
		}
		if !entry.Data.RequestBodyTooBigToHandle {
			t.Error("expected RequestBodyTooBigToHandle flag to be set")
		}
		if entry.Data.RequestBody != nil {
			t.Error("body content should not be logged when over limit")
		}
	})

	t.Run("overflow path propagates Close to original body", func(t *testing.T) {
		largeBody := strings.Repeat("x", int(MaxBodyCapture)+100)
		tracker := &trackingReadCloser{Reader: strings.NewReader(largeBody)}
		req, _ := http.NewRequest("POST", "/v1/chat/completions", tracker)
		req.ContentLength = -1

		// Drive the overflow reconstruction path
		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		if int64(len(bodyBytes)) <= MaxBodyCapture {
			t.Fatal("body should exceed limit")
		}

		origBody := req.Body
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), origBody),
			rc:     origBody,
		}

		// Read full body from reconstructed reader
		downstream, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("downstream read error: %v", err)
		}
		if len(downstream) != len(largeBody) {
			t.Errorf("downstream body length mismatch: expected %d, got %d", len(largeBody), len(downstream))
		}

		// Close and verify propagation
		if err := req.Body.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
		if !tracker.closed {
			t.Error("expected Close to propagate to original body")
		}
	})

	t.Run("io.LimitReader caps memory allocation", func(t *testing.T) {
		// Verify that io.LimitReader prevents reading more than MaxBodyCapture+1 bytes
		largeBody := strings.Repeat("z", int(MaxBodyCapture)*3)
		reader := io.LimitReader(strings.NewReader(largeBody), MaxBodyCapture+1)
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if int64(len(data)) != MaxBodyCapture+1 {
			t.Errorf("expected exactly %d bytes, got %d", MaxBodyCapture+1, len(data))
		}
	})
}

func TestDecompressBodyEmptyInput(t *testing.T) {
	// Empty body should return unchanged
	result, decompressed := decompressBody([]byte{}, "gzip")
	if decompressed {
		t.Error("expected no decompression for empty body")
	}
	if len(result) != 0 {
		t.Error("expected empty result for empty input")
	}

	// Nil body should return unchanged
	result, decompressed = decompressBody(nil, "gzip")
	if decompressed {
		t.Error("expected no decompression for nil body")
	}
	if result != nil {
		t.Error("expected nil result for nil input")
	}
}
