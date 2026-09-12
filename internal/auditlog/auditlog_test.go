package auditlog

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andybalholm/brotli"
	"github.com/labstack/echo/v5"
)

func TestTruncateAttemptErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"short ascii", "boom"},
		{"exactly at limit", strings.Repeat("a", maxAttemptErrorMessageLength)},
		{"long ascii", strings.Repeat("a", maxAttemptErrorMessageLength+50)},
		// A 3-byte rune (✓) straddling the byte limit must not be split.
		{"multibyte straddling limit", strings.Repeat("a", maxAttemptErrorMessageLength-1) + strings.Repeat("✓", 10)},
		{"all multibyte", strings.Repeat("界", maxAttemptErrorMessageLength)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateAttemptErrorMessage(tt.in)
			require.LessOrEqual(t, len(got), maxAttemptErrorMessageLength)
			require.True(t, utf8.ValidString(got), "result is not valid UTF-8: %q", got)
			if len(tt.in) <= maxAttemptErrorMessageLength {
				require.Equal(t, tt.in, got, "short input mutated")
			}
			require.True(t, strings.HasPrefix(tt.in, got))
		})
	}
}

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
			name: "redact authentication transaction from location",
			input: map[string]string{
				"Location": "https://id.example.com/authorize?client_id=gomodel&code_challenge=challenge&nonce=nonce&state=state",
			},
			expected: map[string]string{
				"Location": "https://id.example.com/authorize?client_id=gomodel&code_challenge=REDACTED&nonce=REDACTED&state=REDACTED",
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
		{
			name: "redact provider credential headers",
			input: map[string]string{
				"api-key":        "azure-secret",
				"X-Goog-Api-Key": "gemini-secret",
				"Content-Type":   "application/json",
			},
			expected: map[string]string{
				"api-key":        "[REDACTED]",
				"X-Goog-Api-Key": "[REDACTED]",
				"Content-Type":   "application/json",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactHeaders(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)

				return
			}

			assert.Len(t, result, len(tt.expected))

			for k, v := range tt.expected {
				assert.Equal(t, v, result[k], "header %q", k)
			}
		})
	}
}

func TestLogEntryJSON(t *testing.T) {
	entry := &LogEntry{
		ID:             "test-id-123",
		Timestamp:      time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		DurationNs:     1500000,
		RequestedModel: "friendly-alias",
		ResolvedModel:  "openai/gpt-4",
		Provider:       "openai",
		AliasUsed:      true,
		CacheType:      CacheTypeExact,
		StatusCode:     200,
		RequestID:      "req-123",
		ClientIP:       "192.168.1.1",
		Method:         "POST",
		Path:           "/v1/chat/completions",
		Stream:         false,
		Data: &LogData{
			UserAgent: "test-agent",
			Failover: &FailoverSnapshot{
				TargetModel: "azure/gpt-4o",
			},
		},
	}

	// Test JSON marshaling
	data, err := json.Marshal(entry)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var decoded LogEntry
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify fields
	assert.Equal(t, entry.ID, decoded.ID)
	assert.Equal(t, entry.RequestedModel, decoded.RequestedModel)
	assert.Equal(t, entry.Provider, decoded.Provider)
	assert.Equal(t, entry.ResolvedModel, decoded.ResolvedModel)
	assert.Equal(t, entry.AliasUsed, decoded.AliasUsed)
	assert.Equal(t, entry.CacheType, decoded.CacheType)
	assert.Equal(t, entry.StatusCode, decoded.StatusCode)
	assert.Equal(t, entry.RequestID, decoded.RequestID)
	require.NotNil(t, decoded.Data)
	require.NotNil(t, decoded.Data.Failover)
	assert.Equal(t, "azure/gpt-4o", decoded.Data.Failover.TargetModel)
}

func TestLogDataWithBodies(t *testing.T) {
	// Use interface{} types (maps) for bodies - this is how they're stored now
	requestBody := map[string]any{
		"model":    "gpt-4",
		"messages": []any{},
	}
	responseBody := map[string]any{
		"id":      "resp-123",
		"choices": []any{},
	}

	data := &LogData{
		UserAgent:    "test-agent",
		RequestBody:  requestBody,
		ResponseBody: responseBody,
	}

	// Marshal and unmarshal
	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)

	var decoded LogData
	err = json.Unmarshal(jsonBytes, &decoded)
	require.NoError(t, err)

	// Verify bodies are preserved (decoded as map[string]interface{})
	decodedReqBody, ok := decoded.RequestBody.(map[string]any)
	require.True(t, ok, "RequestBody is not a map, got %T", decoded.RequestBody)
	assert.Equal(t, "gpt-4", decodedReqBody["model"])

	decodedRespBody, ok := decoded.ResponseBody.(map[string]any)
	require.True(t, ok, "ResponseBody is not a map, got %T", decoded.ResponseBody)
	assert.Equal(t, "resp-123", decodedRespBody["id"])
}

// mockStore implements LogStore for testing
type mockStore struct {
	mu      sync.Mutex
	entries []*LogEntry
	closed  bool
}

type failingStore struct {
	err error
}

func (s failingStore) WriteBatch(context.Context, []*LogEntry) error {
	return s.err
}

func (s failingStore) Flush(context.Context) error {
	return nil
}

func (s failingStore) Close() error {
	return nil
}

type capturedAuditLiveEvent struct {
	eventType string
	entry     *LogEntry
}

type capturingAuditLivePublisher struct {
	mu     sync.Mutex
	events []capturedAuditLiveEvent
}

func (p *capturingAuditLivePublisher) PublishAuditEvent(eventType string, entry *LogEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, capturedAuditLiveEvent{eventType: eventType, entry: entry})
}

func (p *capturingAuditLivePublisher) snapshot() []capturedAuditLiveEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	events := make([]capturedAuditLiveEvent, len(p.events))
	copy(events, p.events)
	return events
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
	for i := range 5 {
		logger.Write(&LogEntry{
			ID:             fmt.Sprintf("entry-%d", i),
			Timestamp:      time.Now(),
			RequestedModel: "test-model",
		})
	}

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	// Verify entries were written
	assert.Len(t, store.getEntries(), 5)
}

func TestLoggerFlushBatchPublishesFailedLiveEvent(t *testing.T) {
	publisher := &capturingAuditLivePublisher{}
	logger := &Logger{store: failingStore{err: errors.New("write failed")}}
	logger.SetLivePublisher(publisher)

	entry := &LogEntry{ID: "audit-1", RequestID: "req-1", Timestamp: time.Now()}
	logger.flushBatch([]*LogEntry{entry})

	events := publisher.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, LiveEventAuditFailed, events[0].eventType)
	require.Equal(t, entry, events[0].entry)
}

func TestMiddleware_UsesIngressFrameRequestBodyWithoutReadingStream(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(`{"model":"from-body"}`)}
	c.Request().Body = trackedBody
	c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
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
	))))

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, 0, trackedBody.readCalls)
	require.Len(t, logger.entries, 1)

	requestBody, ok := BodyDocument(logger.entries[0].Data.RequestBody).(map[string]any)
	require.True(t, ok, "RequestBody = %T, want JSON object", logger.entries[0].Data.RequestBody)
	require.Equal(t, "from-ingress", requestBody["model"])
}

func TestMiddleware_UsesIngressTooLargeFlagWithoutReadingStream(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(strings.Repeat("x", 16))}
	c.Request().Body = trackedBody
	c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
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
	))))

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, 0, trackedBody.readCalls)
	require.Len(t, logger.entries, 1)
	require.True(t, logger.entries[0].Data.RequestBodyTooBigToHandle)
	require.Nil(t, logger.entries[0].Data.RequestBody)
}

func TestMiddleware_SkipsStreamingResponseWriterCapture(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4","stream":true}`)

	var capture *responseBodyCapture
	handler := Middleware(logger)(func(c *echo.Context) error {
		var ok bool
		capture, ok = c.Response().(*responseBodyCapture)
		require.True(t, ok, "Response = %T, want *responseBodyCapture", c.Response())

		MarkEntryAsStreaming(c, true)
		EnrichEntryWithStream(c, true)
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().WriteHeader(http.StatusOK)
		if _, err := c.Response().Write([]byte("data: {\"id\":\"chatcmpl-test\"}\n\n")); err != nil {
			return err
		}
		if _, err := c.Response().Write([]byte("data: [DONE]\n\n")); err != nil {
			return err
		}
		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capture)
	require.Equal(t, 0, capture.body.Len())
	require.False(t, capture.truncated)
	require.Empty(t, logger.entries)
}

// TestMiddleware_AudioResponseNotMarkedTruncated verifies that an oversized audio
// response — which trips the response writer's truncation flag — does NOT set
// ResponseBodyTooBigToHandle on the audit entry. The audio handler captures the
// body losslessly via its own path, so a truncation flag here would produce
// conflicting metadata (a fully-stored body alongside a "too big" marker).
func TestMiddleware_AudioResponseNotMarkedTruncated(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true, LogBodies: true}}

	c, _ := echotest.Post(t, "/v1/audio/speech", `{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy"}`)

	oversized := bytes.Repeat([]byte{0xff}, int(MaxBodyCapture)+16)
	var capture *responseBodyCapture
	handler := Middleware(logger)(func(c *echo.Context) error {
		capture, _ = c.Response().(*responseBodyCapture)
		c.Response().Header().Set("Content-Type", "audio/mpeg")
		c.Response().WriteHeader(http.StatusOK)
		_, err := c.Response().Write(oversized)
		return err
	})
	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capture)
	require.True(t, capture.truncated)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.NotNil(t, entry.Data)
	assert.False(t, entry.Data.ResponseBodyTooBigToHandle, "the handler owns audio body capture")
	assert.Nil(t, entry.Data.ResponseBody, "middleware must not store the audio response body")
}

func TestMiddleware_PrefersWorkflowOverLegacyResolution(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"anthropic/claude-opus-4-6"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("anthropic/claude-opus-4-6", ""),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
			ProviderType:     "openai",
			AliasApplied:     true,
		},
	})))

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntry(c, "placeholder", "placeholder")
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "anthropic/claude-opus-4-6", entry.RequestedModel)
	require.Equal(t, "openai/gpt-5-nano", entry.ResolvedModel)
	require.Equal(t, "openai", entry.Provider)
	require.True(t, entry.AliasUsed)
}

func TestMiddleware_UsesWorkflowRequestID(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-5-nano"}`, echotest.WithHeader("X-Request-ID", "header-req-id"))
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		RequestID:    "workflow-req-id",
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("gpt-5-nano", ""),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
			ProviderType:     "openai",
		},
	})))

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "workflow-req-id", entry.RequestID)
}

func TestMiddleware_DoesNotApplyModelMetadataWithoutWorkflow(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"legacy-only"}`)

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Empty(t, entry.RequestedModel)
	require.Empty(t, entry.ResolvedModel)
	require.Empty(t, entry.Provider)
	require.False(t, entry.AliasUsed)
}

func TestMiddleware_PassthroughWorkflowUsesPassthroughModel(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	c, _ := echotest.Post(t, "/p/openai/v1/chat/completions", `{"model":"gpt-4.1-nano"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Mode:         core.ExecutionModePassthrough,
		ProviderType: "openai",
		Passthrough: &core.PassthroughRouteInfo{
			Provider: "openai",
			Model:    "gpt-4.1-nano",
		},
	})))

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntry(c, "placeholder", "placeholder")
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "gpt-4.1-nano", entry.RequestedModel)
	require.Equal(t, "openai", entry.Provider)
	require.Empty(t, entry.ResolvedModel)
}

func TestMiddleware_StoresWorkflowVersionID(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-5-nano"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		ProviderType: "openai",
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-version-123",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: true,
			},
		},
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("gpt-5-nano", ""),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
			ProviderType:     "openai",
		},
	})))

	handler := Middleware(logger)(func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "workflow-version-123", logger.entries[0].WorkflowVersionID)
}

func TestMiddleware_StoresAuthKeyIDFromContext(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true}}
	middleware := Middleware(logger)

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini"}`)
	c.SetRequest(c.Request().WithContext(core.WithAuthKeyID(c.Request().Context(), "key-123")))

	handler := middleware(func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "key-123", logger.entries[0].AuthKeyID)
}

func TestMiddleware_StoresEffectiveUserPathFromContext(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true}}
	middleware := Middleware(logger)

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini"}`)
	ctx := core.WithRequestSnapshot(c.Request().Context(), &core.RequestSnapshot{UserPath: "/team/from-header"})
	ctx = core.WithEffectiveUserPath(ctx, "/team/from-auth-key")
	c.SetRequest(c.Request().WithContext(ctx))

	handler := middleware(func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "/team/from-auth-key", logger.entries[0].UserPath)
}

func TestMiddleware_DefaultsMissingUserPathToRoot(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true}}
	middleware := Middleware(logger)

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-4o-mini"}`)

	handler := middleware(func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "/", logger.entries[0].UserPath)
}

func TestMiddleware_SkipsWriteWhenWorkflowDisablesAudit(t *testing.T) {
	logger := &capturingLogger{
		cfg: Config{Enabled: true, LogBodies: true, LogHeaders: true},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-5-nano"}`)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-version-123",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      false,
				Usage:      true,
				Guardrails: true,
			},
		},
	})))

	handler := Middleware(logger)(func(c *echo.Context) error {
		entry := c.Get(string(LogEntryKey))
		require.Nil(t, entry)

		return c.NoContent(http.StatusNoContent)
	})
	err := handler(c)
	require.NoError(t, err)
	require.Empty(t, logger.entries)
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
	assert.Len(t, store.getEntries(), 1)

	// Verify store was closed
	assert.True(t, store.isClosed())
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
		{"audio speech", "/v1/audio/speech", true},
		{"audio transcriptions", "/v1/audio/transcriptions", true},
		{"audio translations", "/v1/audio/translations", true},
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
			result := core.IsModelInteractionPath(tt.path)
			assert.Equal(t, tt.expected, result, "core.IsModelInteractionPath(%q) = %v, want %v", tt.path, result, tt.expected)
		})
	}
}

func TestStreamLogObserver(t *testing.T) {
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

	entry := &LogEntry{
		ID:             "test-entry",
		Timestamp:      time.Now(),
		RequestedModel: "gpt-4",
		Data:           &LogData{},
	}

	observedStream := streaming.NewObservedSSEStream(
		stream,
		NewStreamLogObserver(logger, entry, "/v1/chat/completions"),
	)

	// Read all content
	var buf bytes.Buffer
	_, err := io.Copy(&buf, observedStream)
	require.NoError(t, err)
	// Close stream to trigger logging
	require.NoError(t, observedStream.Close())
	err = logger.Close()
	require.NoError(t, err)

	// Verify entry was logged
	assert.Len(t, store.getEntries(), 1)
}

func TestStreamLogObserverDefaultsMissingChatRoleToAssistant(t *testing.T) {
	streamContent := `data: {"id":"chatcmpl-123","model":"claude-sonnet","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-123","model":"claude-sonnet","choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	logger := &capturingLogger{cfg: Config{Enabled: true, LogBodies: true}}
	entry := &LogEntry{
		ID:        "test-entry",
		Timestamp: time.Now(),
		Data:      &LogData{},
	}

	observedStream := streaming.NewObservedSSEStream(
		io.NopCloser(strings.NewReader(streamContent)),
		NewStreamLogObserver(logger, entry, "/v1/chat/completions"),
	)
	_, err := io.Copy(io.Discard, observedStream)
	require.NoError(t, err)
	err = observedStream.Close()
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	response, ok := logger.entries[0].Data.ResponseBody.(map[string]any)
	require.True(t, ok, "response body type = %T, want map[string]any", logger.entries[0].Data.ResponseBody)

	choices, ok := response["choices"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, choices, 1)

	message, ok := choices[0]["message"].(map[string]any)
	require.True(t, ok, "message = %#v, want map[string]any", choices[0]["message"])
	require.Equal(t, "assistant", message["role"])
}

func TestStreamResponseBuilderChatTextOnly(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-text","model":"gpt-4o-mini","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-text","model":"gpt-4o-mini","created":123,"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
	)

	choice := firstChatStreamChoiceForTest(t, response)
	message := chatStreamMessageForTest(t, choice)
	require.Equal(t, "Hello world", message["content"])
	_, ok := message["tool_calls"]
	require.False(t, ok, "message.tool_calls present for text-only stream: %#v", message["tool_calls"])
	require.Equal(t, "stop", choice["finish_reason"])
}

func TestStreamResponseBuilderChatToolCallOnly(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-tools","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tools","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tools","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	choice := firstChatStreamChoiceForTest(t, response)
	message := chatStreamMessageForTest(t, choice)
	got := message["content"]
	require.Nil(t, got)

	toolCall := firstChatStreamToolCallForTest(t, message)
	_, ok := toolCall["index"]
	require.False(t, ok, "final message tool_call contains streaming index: %#v", toolCall)
	require.Equal(t, "call_1", toolCall["id"])

	function := chatStreamFunctionForTest(t, toolCall)
	require.Equal(t, "get_weather", function["name"])
	require.Equal(t, `{"city":"Paris"}`, function["arguments"])
	require.Equal(t, "tool_calls", choice["finish_reason"])
}

func TestStreamResponseBuilderChatInterleavesTextAndToolCall(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-mixed","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Checking weather.\n"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-mixed","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-mixed","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Calling tool.","tool_calls":[{"index":0,"function":{"arguments":"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	choice := firstChatStreamChoiceForTest(t, response)
	message := chatStreamMessageForTest(t, choice)
	require.Equal(t, "Checking weather.\nCalling tool.", message["content"])

	function := chatStreamFunctionForTest(t, firstChatStreamToolCallForTest(t, message))
	require.Equal(t, `{"city":"Paris"}`, function["arguments"])
}

func TestStreamResponseBuilderChatParallelToolCalls(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-parallel","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"lookup_time","arguments":"{\"city\":\""}},{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-parallel","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"Paris\"}"}},{"index":1,"function":{"arguments":"Warsaw\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	message := chatStreamMessageForTest(t, firstChatStreamChoiceForTest(t, response))
	toolCalls, ok := message["tool_calls"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 2, "tool_calls = %#v, want two tool calls", message["tool_calls"])
	require.Equal(t, "call_a", toolCalls[0]["id"])
	require.Equal(t, "call_b", toolCalls[1]["id"])
	require.Equal(t, `{"city":"Paris"}`, chatStreamFunctionForTest(t, toolCalls[0])["arguments"])
	require.Equal(t, `{"city":"Warsaw"}`, chatStreamFunctionForTest(t, toolCalls[1])["arguments"])
}

func TestStreamResponseBuilderChatSparseChoiceFallbackDoesNotCollide(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-sparse-choice","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"zero"},"finish_reason":"stop"},{"index":2,"delta":{"role":"assistant","content":"two"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-sparse-choice","model":"gpt-4o-mini","choices":[{"delta":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}]}`,
	)

	choices := chatStreamChoicesForTest(t, response)
	require.Len(t, choices, 3)

	for i, want := range []int{0, 2, 3} {
		require.Equal(t, want, choices[i]["index"])
	}
	require.Equal(t, "two", chatStreamMessageForTest(t, choices[1])["content"])
	require.Equal(t, "synthetic", chatStreamMessageForTest(t, choices[2])["content"])
}

func TestStreamResponseBuilderChatSparseToolCallFallbackDoesNotCollide(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-sparse-tools","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},{"index":2,"id":"call_c","type":"function","function":{"name":"lookup_time","arguments":"{\"city\":\"Warsaw\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-sparse-tools","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_synthetic","type":"function","function":{"name":"lookup_air","arguments":"{\"city\":\"Berlin\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	message := chatStreamMessageForTest(t, firstChatStreamChoiceForTest(t, response))
	toolCalls, ok := message["tool_calls"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 3, "tool_calls = %#v, want three tool calls", message["tool_calls"])

	for i, want := range []string{"call_a", "call_c", "call_synthetic"} {
		require.Equal(t, want, toolCalls[i]["id"])
	}
	require.Equal(t, `{"city":"Warsaw"}`, chatStreamFunctionForTest(t, toolCalls[1])["arguments"])
	require.Equal(t, `{"city":"Berlin"}`, chatStreamFunctionForTest(t, toolCalls[2])["arguments"])
}

func TestStreamResponseBuilderChatSkipsToolCallWithoutFunctionDelta(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-orphan-tool","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_keep","type":"function"},{"index":1,"id":"call_drop","type":"function"}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-orphan-tool","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	message := chatStreamMessageForTest(t, firstChatStreamChoiceForTest(t, response))
	toolCall := firstChatStreamToolCallForTest(t, message)
	require.Equal(t, "call_keep", toolCall["id"])

	function := chatStreamFunctionForTest(t, toolCall)
	require.Equal(t, "get_weather", function["name"])
	require.Equal(t, `{"city":"Paris"}`, function["arguments"])
}

func TestStreamResponseBuilderChatCapturesTrailingUsageChunk(t *testing.T) {
	response := buildChatStreamResponseForTest(t,
		`{"id":"chatcmpl-usage","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-usage","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
	)

	usage, ok := response["usage"].(map[string]any)
	require.True(t, ok, "usage = %#v, want object", response["usage"])
	require.Equal(t, float64(7), usage["prompt_tokens"])
	require.Equal(t, float64(2), usage["completion_tokens"])
	require.Equal(t, float64(9), usage["total_tokens"])
}

func TestAppendLimitedStreamTextMarksTruncatedWhenBudgetAlreadyFull(t *testing.T) {
	builder := &streamResponseBuilder{contentLen: MaxContentCapture}
	var dst strings.Builder

	appendLimitedStreamText(builder, &dst, "x")

	require.True(t, builder.truncated)
	require.Empty(t, dst.String())
	require.Equal(t, MaxContentCapture, builder.contentLen)
}

func buildChatStreamResponseForTest(t *testing.T, events ...string) map[string]any {
	t.Helper()

	builder := &streamResponseBuilder{}
	for _, raw := range events {
		var event map[string]any
		err := json.Unmarshal([]byte(raw), &event)
		require.NoError(t, err, "failed to unmarshal event %q: %v", raw, err)

		parseChatCompletionEvent(builder, event)
	}
	return builder.buildChatCompletionResponse()
}

func chatStreamChoicesForTest(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()

	choices, ok := response["choices"].([]map[string]any)
	require.True(t, ok, "choices = %#v, want choice slice", response["choices"])

	return choices
}

func firstChatStreamChoiceForTest(t *testing.T, response map[string]any) map[string]any {
	t.Helper()

	choices := chatStreamChoicesForTest(t, response)
	require.Len(t, choices, 1)

	return choices[0]
}

func chatStreamMessageForTest(t *testing.T, choice map[string]any) map[string]any {
	t.Helper()

	message, ok := choice["message"].(map[string]any)
	require.True(t, ok, "message = %#v, want object", choice["message"])

	return message
}

func firstChatStreamToolCallForTest(t *testing.T, message map[string]any) map[string]any {
	t.Helper()

	toolCalls, ok := message["tool_calls"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 1, "tool_calls = %#v, want one tool call", message["tool_calls"])

	return toolCalls[0]
}

func chatStreamFunctionForTest(t *testing.T, toolCall map[string]any) map[string]any {
	t.Helper()

	function, ok := toolCall["function"].(map[string]any)
	require.True(t, ok, "tool_call.function = %#v, want object", toolCall["function"])

	return function
}

func TestNewStreamLogObserverNilInputs(t *testing.T) {
	observer := NewStreamLogObserver(nil, &LogEntry{}, "/v1/chat/completions")
	assert.Nil(t, observer)
	observer = NewStreamLogObserver(&NoopLogger{}, nil, "/v1/chat/completions")
	assert.Nil(t, observer)
}

func TestCreateStreamEntry(t *testing.T) {
	// Test nil input
	result := CreateStreamEntry(context.Background(), nil)
	assert.Nil(t, result)

	// Test with valid entry
	baseEntry := &LogEntry{
		ID:                "test-id",
		Timestamp:         time.Now(),
		DurationNs:        1000,
		RequestedModel:    "claude-opus-4-6",
		ResolvedModel:     "openai/gpt-5-nano",
		Provider:          "openai",
		ProviderName:      "primary-openai",
		AliasUsed:         true,
		WorkflowVersionID: "workflow-version-123",
		CacheType:         CacheTypeSemantic,
		StatusCode:        200,
		RequestID:         "req-123",
		AuthKeyID:         "auth-key-123",
		AuthMethod:        AuthMethodAPIKey,
		ClientIP:          "127.0.0.1",
		Method:            "POST",
		Path:              "/v1/chat/completions",
		UserPath:          "/team/alpha",
		Stream:            false,
		Data: &LogData{
			UserAgent: "test",
			WorkflowFeatures: &WorkflowFeaturesSnapshot{
				Cache:      false,
				Audit:      true,
				Usage:      true,
				Guardrails: false,
				Failover:   true,
			},
			Failover: &FailoverSnapshot{
				TargetModel: "azure/gpt-4o",
			},
			RequestHeaders: map[string]string{
				"Content-Type": "application/json",
			},
		},
	}

	streamEntry := CreateStreamEntry(context.Background(), baseEntry)
	require.NotNil(t, streamEntry)

	// Verify fields are copied
	assert.Equal(t, baseEntry.ID, streamEntry.ID)
	assert.Equal(t, baseEntry.RequestedModel, streamEntry.RequestedModel)
	assert.Equal(t, baseEntry.ResolvedModel, streamEntry.ResolvedModel)
	assert.Equal(t, baseEntry.ProviderName, streamEntry.ProviderName)
	assert.Equal(t, baseEntry.AliasUsed, streamEntry.AliasUsed)
	assert.Equal(t, baseEntry.CacheType, streamEntry.CacheType)
	assert.Equal(t, baseEntry.WorkflowVersionID, streamEntry.WorkflowVersionID)
	assert.True(t, streamEntry.Stream)
	assert.Equal(t, baseEntry.RequestID, streamEntry.RequestID)
	assert.Equal(t, baseEntry.AuthKeyID, streamEntry.AuthKeyID)
	assert.Equal(t, baseEntry.AuthMethod, streamEntry.AuthMethod)
	require.NotNil(t, streamEntry.Data)
	require.NotNil(t, streamEntry.Data.Failover)
	assert.Equal(t, "azure/gpt-4o", streamEntry.Data.Failover.TargetModel)
	assert.Equal(t, baseEntry.ClientIP, streamEntry.ClientIP)
	assert.Equal(t, baseEntry.Method, streamEntry.Method)
	assert.Equal(t, baseEntry.Path, streamEntry.Path)
	assert.Equal(t, baseEntry.UserPath, streamEntry.UserPath)

	// Verify headers are copied (not same reference)
	require.NotNil(t, streamEntry.Data.RequestHeaders)
	baseEntry.Data.RequestHeaders["New"] = "value"
	assert.NotEqual(t, "value", streamEntry.Data.RequestHeaders["New"])
	require.NotNil(t, streamEntry.Data.WorkflowFeatures)
	require.NotSame(t, baseEntry.Data.WorkflowFeatures, streamEntry.Data.WorkflowFeatures)
	assert.Equal(t, baseEntry.Data.WorkflowFeatures.Cache, streamEntry.Data.WorkflowFeatures.Cache)
	assert.Equal(t, baseEntry.Data.WorkflowFeatures.Audit, streamEntry.Data.WorkflowFeatures.Audit)
	assert.Equal(t, baseEntry.Data.WorkflowFeatures.Usage, streamEntry.Data.WorkflowFeatures.Usage)
	assert.Equal(t, baseEntry.Data.WorkflowFeatures.Guardrails, streamEntry.Data.WorkflowFeatures.Guardrails)
	assert.Equal(t, baseEntry.Data.WorkflowFeatures.Failover, streamEntry.Data.WorkflowFeatures.Failover)
}

func TestEnrichEntryWithWorkflowStoresWorkflowFeatures(t *testing.T) {
	c, _ := echotest.Get(t, "/")

	entry := &LogEntry{ID: "workflow-audit-entry"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v3",
			Features: core.WorkflowFeatures{
				Cache:      false,
				Audit:      true,
				Usage:      false,
				Guardrails: true,
				Failover:   false,
			},
		},
	})

	require.Equal(t, "workflow-v3", entry.WorkflowVersionID)
	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.WorkflowFeatures)
	require.False(t, entry.Data.WorkflowFeatures.Cache)
	require.True(t, entry.Data.WorkflowFeatures.Audit)
	require.False(t, entry.Data.WorkflowFeatures.Usage)
	require.True(t, entry.Data.WorkflowFeatures.Guardrails)
	require.False(t, entry.Data.WorkflowFeatures.Failover)
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
				assert.Empty(t, result)

			} else {
				assert.NotEmpty(t, result)
				assert.Len(t, result, 16)
			}
		})
	}

	// Test consistency - same input should produce same hash
	hash1 := hashAPIKey("Bearer test-key")
	hash2 := hashAPIKey("Bearer test-key")
	assert.Equal(t, hash2, hash1)

	// Test different inputs produce different hashes
	hash3 := hashAPIKey("Bearer different-key")
	assert.NotEqual(t, hash3, hash1)
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

			assert.Equal(t, tt.shouldDecompress, decompressed)

			if tt.shouldDecompress {
				assert.Equal(t, originalData, result)
			}
		})
	}
}

func TestDecompressBodyInvalidData(t *testing.T) {
	// Invalid compressed data should return original
	invalidData := []byte("not valid compressed data")

	result, decompressed := decompressBody(invalidData, "gzip")
	assert.False(t, decompressed)
	assert.Equal(t, invalidData, result)
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
	require.NoError(t, err)
	assert.Equal(t, len(largeData), n)

	// Buffer should be capped at exactly MaxBodyCapture
	assert.Equal(t, int(MaxBodyCapture), capture.body.Len())
	assert.True(t, capture.truncated)
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
	assert.False(t, capture.truncated)
	assert.Equal(t, chunkSize, capture.body.Len())

	// Second chunk: fits exactly (no data lost, so truncated remains false)
	_, _ = capture.Write(chunk)
	assert.False(t, capture.truncated)
	assert.Equal(t, int(MaxBodyCapture), capture.body.Len())

	// Third chunk: entirely skipped, truncated flag set
	_, _ = capture.Write(chunk)
	assert.True(t, capture.truncated)
	assert.Equal(t, int(MaxBodyCapture), capture.body.Len(), "buffer must stay capped after third chunk")
}

func TestResponseBodyCapture_Write_SkipsWhenDisabled(t *testing.T) {
	capture := &responseBodyCapture{
		ResponseWriter: &discardWriter{},
		body:           &bytes.Buffer{},
		shouldCapture: func() bool {
			return false
		},
	}

	payload := []byte(`data: {"chunk":1}` + "\n\n")
	n, err := capture.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, 0, capture.body.Len())
	require.False(t, capture.truncated)
}

func TestHasResponseBodyCaptureHandlesWrappedAndCyclicWriters(t *testing.T) {
	capture := &responseBodyCapture{
		ResponseWriter: &discardWriter{},
		body:           &bytes.Buffer{},
	}
	wrapped := &unwrapTestWriter{ResponseWriter: &discardWriter{}, next: capture}
	require.True(t, hasResponseBodyCapture(wrapped))

	self := &selfUnwrapTestWriter{ResponseWriter: &discardWriter{}}
	require.False(t, hasResponseBodyCapture(self))

	first := &unwrapTestWriter{ResponseWriter: &discardWriter{}}
	second := &unwrapTestWriter{ResponseWriter: &discardWriter{}, next: first}
	first.next = second
	require.False(t, hasResponseBodyCapture(first))
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

type chainReadCloser struct {
	io.Reader
	closer io.Closer
}

func (c *chainReadCloser) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

// discardWriter implements http.ResponseWriter but discards all output.
type discardWriter struct{}

func (d *discardWriter) Header() http.Header         { return http.Header{} }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

type unwrapTestWriter struct {
	http.ResponseWriter
	next http.ResponseWriter
}

func (w *unwrapTestWriter) Unwrap() http.ResponseWriter {
	return w.next
}

type selfUnwrapTestWriter struct {
	http.ResponseWriter
}

func (w *selfUnwrapTestWriter) Unwrap() http.ResponseWriter {
	return w
}

func TestLimitedReaderRequestBodyCapture(t *testing.T) {
	t.Run("chunked request body under limit is captured", func(t *testing.T) {
		body := `{"model":"gpt-4","messages":[]}`
		req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.ContentLength = -1 // Simulate chunked encoding

		entry := &LogEntry{Data: &LogData{}}
		// Simulate the middleware body capture logic
		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		require.NoError(t, err)
		require.LessOrEqual(t, int64(len(bodyBytes)), int64(MaxBodyCapture))

		var parsed any
		if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
			entry.Data.RequestBody = parsed
		}

		assert.NotNil(t, entry.Data.RequestBody)
		assert.False(t, entry.Data.RequestBodyTooBigToHandle)
	})

	t.Run("chunked request body over limit sets flag and preserves downstream body", func(t *testing.T) {
		// Create a body larger than MaxBodyCapture
		largeBody := strings.Repeat("x", int(MaxBodyCapture)+100)
		req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(largeBody))
		req.ContentLength = -1 // Simulate chunked encoding

		entry := &LogEntry{Data: &LogData{}}

		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		require.NoError(t, err)
		require.Greater(t, int64(len(bodyBytes)), int64(MaxBodyCapture))

		entry.Data.RequestBodyTooBigToHandle = true
		// Reconstruct body for downstream
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), req.Body))

		// Verify downstream can read the full body
		downstream, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Len(t, downstream, len(largeBody))
		assert.True(t, entry.Data.RequestBodyTooBigToHandle)
		assert.Nil(t, entry.Data.RequestBody)
	})

	t.Run("overflow path propagates Close to original body", func(t *testing.T) {
		largeBody := strings.Repeat("x", int(MaxBodyCapture)+100)
		tracker := &trackingReadCloser{Reader: strings.NewReader(largeBody)}
		req, _ := http.NewRequest("POST", "/v1/chat/completions", tracker)
		req.ContentLength = -1

		// Drive the overflow reconstruction path
		limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
		bodyBytes, err := io.ReadAll(limitedReader)
		require.NoError(t, err)
		require.Greater(t, int64(len(bodyBytes)), int64(MaxBodyCapture))

		origBody := req.Body
		req.Body = &chainReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), origBody),
			closer: origBody,
		}

		// Read full body from reconstructed reader
		downstream, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Len(t, downstream, len(largeBody))
		// Close and verify propagation
		require.NoError(t, req.Body.Close())
		assert.True(t, tracker.closed)
	})

	t.Run("io.LimitReader caps memory allocation", func(t *testing.T) {
		// Verify that io.LimitReader prevents reading more than MaxBodyCapture+1 bytes
		largeBody := strings.Repeat("z", int(MaxBodyCapture)*3)
		reader := io.LimitReader(strings.NewReader(largeBody), MaxBodyCapture+1)
		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Len(t, data, int(MaxBodyCapture)+1)
	})
}

func TestDecompressBodyEmptyInput(t *testing.T) {
	// Empty body should return unchanged
	result, decompressed := decompressBody([]byte{}, "gzip")
	assert.False(t, decompressed)
	assert.Empty(t, result)

	// Nil body should return unchanged
	result, decompressed = decompressBody(nil, "gzip")
	assert.False(t, decompressed)
	assert.Nil(t, result)
}
