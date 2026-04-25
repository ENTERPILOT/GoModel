//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/server"
)

// mockLogStore is an in-memory log store for testing
type mockLogStore struct {
	mu      sync.Mutex
	entries []*auditlog.LogEntry
}

func newMockLogStore() *mockLogStore {
	return &mockLogStore{
		entries: make([]*auditlog.LogEntry, 0),
	}
}

func (m *mockLogStore) WriteBatch(_ context.Context, entries []*auditlog.LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	return nil
}

func (m *mockLogStore) Flush(_ context.Context) error {
	return nil
}

func (m *mockLogStore) Close() error {
	return nil
}

func (m *mockLogStore) GetEntries() []*auditlog.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid race conditions
	result := make([]*auditlog.LogEntry, len(m.entries))
	copy(result, m.entries)
	return result
}

// GetAPIEntries returns only entries for API paths (excludes health checks)
func (m *mockLogStore) GetAPIEntries() []*auditlog.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*auditlog.LogEntry
	for _, entry := range m.entries {
		if entry.Path != "/health" {
			result = append(result, entry)
		}
	}
	return result
}

func (m *mockLogStore) WaitForEntries(count int, timeout time.Duration) []*auditlog.LogEntry {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries := m.GetEntries()
		if len(entries) >= count {
			return entries
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m.GetEntries()
}

// WaitForAPIEntries waits for API entries (excludes health checks)
func (m *mockLogStore) WaitForAPIEntries(count int, timeout time.Duration) []*auditlog.LogEntry {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries := m.GetAPIEntries()
		if len(entries) >= count {
			return entries
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m.GetAPIEntries()
}

// setupAuditLogTestServer creates a test server with audit logging enabled
func setupAuditLogTestServer(t *testing.T, cfg auditlog.Config, store *mockLogStore) (string, func()) {
	t.Helper()

	// Reserve a loopback listener up front so the port cannot be stolen before
	// the server starts accepting connections.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Create test provider and registry
	testProvider := NewTestProvider(mockLLMURL, "sk-test-key-12345")
	registry := providers.NewModelRegistry()
	registry.RegisterProvider(testProvider)

	ctx := context.Background()
	require.NoError(t, registry.Initialize(ctx))

	router, err := providers.NewRouter(registry)
	require.NoError(t, err)

	// Create logger with the mock store
	logger := auditlog.NewLogger(store, cfg)

	// Create server with audit logging
	srv := server.New(router, &server.Config{
		AuditLogger: logger,
	})

	// Start server (bind to loopback only)
	serverURL := "http://" + listener.Addr().String()
	serverCtx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- srv.StartWithListener(serverCtx, listener)
	}()

	// Wait for server to be ready
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 30; i++ {
		resp, err := client.Get(serverURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("server shutdown timed out")
		}
		require.NoError(t, logger.Close())
	}

	return serverURL, cleanup
}

func TestAuditLogMiddleware(t *testing.T) {
	t.Run("captures basic request metadata", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait for log entry to be written
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1, "Expected 1 log entry")

		entry := entries[0]
		assert.NotEmpty(t, entry.ID)
		assert.NotZero(t, entry.Timestamp)
		assert.Greater(t, entry.DurationNs, int64(0))
		assert.Equal(t, http.StatusOK, entry.StatusCode)
		assert.Equal(t, "POST", entry.Method)
		assert.Equal(t, "/v1/chat/completions", entry.Path)
		assert.NotEmpty(t, entry.RequestID)
	})

	t.Run("captures request and response bodies when enabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     true,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request
		payload := defaultChatReq("Test message")
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.NotNil(t, entry.Data.RequestBody)
		assert.NotNil(t, entry.Data.ResponseBody)

		// Verify request body contains our message (now stored as interface{})
		reqBody, ok := entry.Data.RequestBody.(map[string]interface{})
		require.True(t, ok, "RequestBody should be a map[string]interface{}, got %T", entry.Data.RequestBody)
		assert.Equal(t, "gpt-4", reqBody["model"])
	})

	t.Run("captures headers with redaction when enabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    true,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request with Authorization header
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", serverURL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-secret-key-12345")
		req.Header.Set("X-Custom-Header", "custom-value")

		client := &http.Client{}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.NotNil(t, entry.Data.RequestHeaders)

		// Authorization header should be redacted
		assert.Equal(t, "[REDACTED]", entry.Data.RequestHeaders["Authorization"])

		// Custom header should not be redacted
		assert.Equal(t, "custom-value", entry.Data.RequestHeaders["X-Custom-Header"])
	})

	t.Run("does not log when disabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       false, // Disabled
			LogBodies:     true,
			LogHeaders:    true,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait a bit and verify no API entries were logged
		time.Sleep(500 * time.Millisecond)
		entries := store.GetAPIEntries()
		assert.Len(t, entries, 0, "Expected no API log entries when logging is disabled")
	})

	t.Run("hashes API key for identification", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request with Authorization header
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", serverURL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-test-api-key")

		client := &http.Client{}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer closeBody(resp)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		// API key hash should be present (16 chars of SHA256 for 64 bits of entropy)
		assert.NotEmpty(t, entry.Data.APIKeyHash)
		assert.Len(t, entry.Data.APIKeyHash, 16)
		// Hash should NOT contain the actual key
		assert.NotContains(t, entry.Data.APIKeyHash, "sk-test")
	})
}

func TestAuditLogStreaming(t *testing.T) {
	t.Run("logs streaming requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a streaming request
		payload := defaultChatReq("Count to 3")
		payload.Stream = true
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Read the stream to completion
		_ = readStreamingResponse(t, resp.Body)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.Equal(t, http.StatusOK, entry.StatusCode)
		assert.Equal(t, "/v1/chat/completions", entry.Path)
	})

	t.Run("captures response headers for streaming requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    true, // Enable header logging
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a streaming request
		payload := defaultChatReq("Hello")
		payload.Stream = true
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Read the stream to completion
		_ = readStreamingResponse(t, resp.Body)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]

		// Verify request headers are captured
		assert.NotNil(t, entry.Data.RequestHeaders)
		assert.Equal(t, "application/json", entry.Data.RequestHeaders["Content-Type"])

		// Verify response headers are captured for streaming
		assert.NotNil(t, entry.Data.ResponseHeaders, "ResponseHeaders should be captured for streaming requests")
		assert.Equal(t, "text/event-stream", entry.Data.ResponseHeaders["Content-Type"])
		assert.Equal(t, "no-cache", entry.Data.ResponseHeaders["Cache-Control"])
		assert.Equal(t, "keep-alive", entry.Data.ResponseHeaders["Connection"])
	})

	t.Run("captures duration for streaming requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a streaming request
		payload := defaultChatReq("Hello")
		payload.Stream = true
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Read the stream to completion
		_ = readStreamingResponse(t, resp.Body)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]

		// Verify duration is captured (should be > 0 since streaming takes time)
		assert.Greater(t, entry.DurationNs, int64(0), "DurationNs should be captured for streaming requests")
		// Duration should be reasonable (less than 10 seconds for this test)
		assert.Less(t, entry.DurationNs, int64(10*time.Second), "DurationNs should be reasonable")
	})
}

func TestAuditLogConcurrency(t *testing.T) {
	t.Run("handles concurrent requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    1000,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		const numRequests = 20
		var wg sync.WaitGroup
		wg.Add(numRequests)

		for i := 0; i < numRequests; i++ {
			go func(idx int) {
				defer wg.Done()
				payload := defaultChatReq(fmt.Sprintf("Request %d", idx))
				body, _ := json.Marshal(payload)
				resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
				if err != nil {
					t.Logf("Request %d failed: %v", idx, err)
					return
				}
				closeBody(resp)
			}(i)
		}

		wg.Wait()

		// Wait for all log entries
		entries := store.WaitForAPIEntries(numRequests, 5*time.Second)
		assert.Len(t, entries, numRequests, "Expected all requests to be logged")
		// Verify all entries have unique IDs
		ids := make(map[string]bool)
		for _, entry := range entries {
			assert.NotEmpty(t, entry.ID)
			assert.False(t, ids[entry.ID], "Duplicate entry ID found")
			ids[entry.ID] = true
		}
	})
}

func TestAuditLogHeaderRedaction(t *testing.T) {
	// Test that all sensitive headers are properly redacted
	sensitiveHeaders := []string{
		"Authorization",
		"X-Api-Key",
		"Cookie",
		"X-Auth-Token",
		"X-Access-Token",
		"Proxy-Authorization",
		"X-Gomodel-Key",
	}

	for _, header := range sensitiveHeaders {
		t.Run(fmt.Sprintf("redacts %s header", header), func(t *testing.T) {
			store := newMockLogStore()
			cfg := auditlog.Config{
				Enabled:       true,
				LogBodies:     false,
				LogHeaders:    true,
				BufferSize:    100,
				FlushInterval: 100 * time.Millisecond,
			}

			serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
			defer cleanup()

			payload := defaultChatReq("Hello")
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest("POST", serverURL+"/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(header, "sensitive-secret-value")

			client := &http.Client{}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer closeBody(resp)

			entries := store.WaitForAPIEntries(1, 2*time.Second)
			require.Len(t, entries, 1)

			entry := entries[0]
			require.NotNil(t, entry.Data.RequestHeaders)

			// The header should be redacted
			assert.Equal(t, "[REDACTED]", entry.Data.RequestHeaders[header],
				"Header %s should be redacted", header)
		})
	}
}

func TestAuditLogErrorCapture(t *testing.T) {
	t.Run("logs failed requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     true,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request with an unsupported model
		payload := core.ChatRequest{
			Model: "unsupported-model-xyz",
			Messages: []core.Message{
				{Role: "user", Content: "Hello"},
			},
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.Equal(t, http.StatusBadRequest, entry.StatusCode)
		assert.Equal(t, "/v1/chat/completions", entry.Path)
		assert.Equal(t, "unsupported-model-xyz", entry.RequestedModel)
		assert.Equal(t, "invalid_request_error", entry.ErrorType)
		assert.Equal(t, "", entry.Provider)
	})

	t.Run("logs unsupported passthrough provider requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     true,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		payload := map[string]any{
			"model": "gpt-4.1-nano",
			"input": "Reply with exactly QA_INVALID_PROVIDER",
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/p/unknown/responses", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.Equal(t, http.StatusBadRequest, entry.StatusCode)
		assert.Equal(t, "/p/unknown/responses", entry.Path)
		assert.Equal(t, "gpt-4.1-nano", entry.RequestedModel)
		assert.Equal(t, "unknown", entry.Provider)
		assert.Equal(t, "invalid_request_error", entry.ErrorType)
	})

	t.Run("logs 405 for wrong HTTP method", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     false,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Send GET to a POST-only endpoint
		resp, err := http.Get(serverURL + "/v1/chat/completions")
		require.NoError(t, err)
		defer closeBody(resp)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.Equal(t, http.StatusMethodNotAllowed, entry.StatusCode)
		assert.Equal(t, "GET", entry.Method)
		assert.Equal(t, "/v1/chat/completions", entry.Path)
	})

	t.Run("logs invalid JSON requests", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:       true,
			LogBodies:     true,
			LogHeaders:    false,
			BufferSize:    100,
			FlushInterval: 100 * time.Millisecond,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Send invalid JSON
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json",
			bytes.NewReader([]byte(`{"model": "gpt-4", invalid json}`)))
		require.NoError(t, err)
		defer closeBody(resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		// Wait for log entry
		entries := store.WaitForAPIEntries(1, 2*time.Second)
		require.Len(t, entries, 1)

		entry := entries[0]
		assert.Equal(t, http.StatusBadRequest, entry.StatusCode)
	})
}

func TestAuditLogOnlyModelInteractions(t *testing.T) {
	t.Run("logs model endpoints when OnlyModelInteractions enabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:               true,
			LogBodies:             false,
			LogHeaders:            false,
			BufferSize:            100,
			FlushInterval:         100 * time.Millisecond,
			OnlyModelInteractions: true,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make a request to a model endpoint
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait for log entry to be written
		entries := store.WaitForEntries(1, 2*time.Second)
		require.Len(t, entries, 1, "Expected 1 log entry for model endpoint")

		entry := entries[0]
		assert.Equal(t, "/v1/chat/completions", entry.Path)
	})

	t.Run("skips health endpoint when OnlyModelInteractions enabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:               true,
			LogBodies:             false,
			LogHeaders:            false,
			BufferSize:            100,
			FlushInterval:         100 * time.Millisecond,
			OnlyModelInteractions: true,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make multiple requests to health endpoint
		for i := 0; i < 3; i++ {
			resp, err := http.Get(serverURL + "/health")
			require.NoError(t, err)
			closeBody(resp)
		}

		// Wait a bit and check that NO entries were logged
		time.Sleep(500 * time.Millisecond)
		entries := store.GetEntries()
		assert.Empty(t, entries, "Expected no log entries for health endpoint when OnlyModelInteractions=true")
	})

	t.Run("logs health endpoint when OnlyModelInteractions disabled", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:               true,
			LogBodies:             false,
			LogHeaders:            false,
			BufferSize:            100,
			FlushInterval:         100 * time.Millisecond,
			OnlyModelInteractions: false,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Note: setupAuditLogTestServer makes health check calls during startup
		// so we may already have entries. Count before making our request.
		time.Sleep(200 * time.Millisecond) // Wait for any buffered entries to flush
		entriesBefore := len(store.GetEntries())

		// Make requests to health endpoint
		resp, err := http.Get(serverURL + "/health")
		require.NoError(t, err)
		closeBody(resp)

		// Wait for log entry to be written (at least 1 more than before)
		entries := store.WaitForEntries(entriesBefore+1, 2*time.Second)
		require.Greater(t, len(entries), entriesBefore, "Expected new log entry for health endpoint when OnlyModelInteractions=false")

		// The last entry should be our health request
		lastEntry := entries[len(entries)-1]
		assert.Equal(t, "/health", lastEntry.Path)
	})

	t.Run("filters mixed requests correctly", func(t *testing.T) {
		store := newMockLogStore()
		cfg := auditlog.Config{
			Enabled:               true,
			LogBodies:             false,
			LogHeaders:            false,
			BufferSize:            100,
			FlushInterval:         100 * time.Millisecond,
			OnlyModelInteractions: true,
		}

		serverURL, cleanup := setupAuditLogTestServer(t, cfg, store)
		defer cleanup()

		// Make multiple health requests (should not be logged)
		for i := 0; i < 5; i++ {
			resp, err := http.Get(serverURL + "/health")
			require.NoError(t, err)
			closeBody(resp)
		}

		// Make a model request (should be logged)
		payload := defaultChatReq("Hello")
		body, _ := json.Marshal(payload)
		resp, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		closeBody(resp)

		// Make more health requests (should not be logged)
		for i := 0; i < 3; i++ {
			resp, err := http.Get(serverURL + "/health")
			require.NoError(t, err)
			closeBody(resp)
		}

		// Wait for log entry to be written
		entries := store.WaitForEntries(1, 2*time.Second)

		// Should only have the model endpoint logged
		require.Len(t, entries, 1, "Expected only 1 log entry (model endpoint)")
		assert.Equal(t, "/v1/chat/completions", entries[0].Path)
	})
}
