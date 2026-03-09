//go:build stress

package stress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gomodel/internal/cache/modelcache"
	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/server"
)

// =============================================================================
// TEST 1: Race condition in InitializeAsync
// =============================================================================

// mockProvider implements core.Provider for testing
type mockProvider struct {
	models    []core.Model
	delay     time.Duration
	callCount atomic.Int64
}

func (m *mockProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (m *mockProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	m.callCount.Add(1)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   m.models,
	}, nil
}

func (m *mockProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (m *mockProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func TestRegistryRaceCondition(t *testing.T) {
	// This test attempts to trigger concurrent map access panics between:
	// - Background goroutine writing to r.models in Initialize()
	// - Concurrent reads via GetProvider()
	// Note: Run with -race flag for complete data race detection

	registry := providers.NewModelRegistry()

	// Create mock provider with many models
	mp := &mockProvider{
		models: make([]core.Model, 100),
		delay:  10 * time.Millisecond, // Add delay to increase race window
	}
	for i := 0; i < 100; i++ {
		mp.models[i] = core.Model{ID: fmt.Sprintf("model-%d", i)}
	}

	registry.RegisterProviderWithType(mp, "mock")

	// Start async initialization
	ctx := context.Background()
	registry.InitializeAsync(ctx)

	// Immediately start hammering with concurrent reads
	var wg sync.WaitGroup
	panicked := atomic.Bool{}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked.Store(true)
					t.Errorf("PANIC detected (goroutine %d): %v", id, r)
				}
			}()

			// Rapid reads during initialization
			for j := 0; j < 1000; j++ {
				_ = registry.GetProvider(fmt.Sprintf("model-%d", j%100))
				_ = registry.ModelCount()
				_ = registry.ListModels()
				runtime.Gosched() // Yield to increase interleaving
			}
		}(i)
	}

	wg.Wait()

	if panicked.Load() {
		t.Fatal("Race condition detected - concurrent map access panic occurred")
	}

	t.Log("No race detected in this run (run with -race flag for definitive check)")
}

// =============================================================================
// TEST 2: Public endpoints don't require authentication
// =============================================================================

func TestPublicEndpointsUnauthenticated(t *testing.T) {
	// Create a mock provider that implements RoutableProvider
	mp := &mockRoutableProvider{}

	// Create server with master key auth enabled
	cfg := &server.Config{
		MasterKey:       "test-secret-key",
		MetricsEnabled:  true,
		MetricsEndpoint: "/metrics",
	}
	srv := server.New(mp, cfg)

	// Test health endpoint WITHOUT auth header - should be accessible
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/health should be public, got %d (expected 200)", rec.Code)
	} else {
		t.Logf("/health returned %d (correctly public)", rec.Code)
	}

	// Test metrics endpoint WITHOUT auth header - should be accessible
	req = httptest.NewRequest("GET", "/metrics", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/metrics should be public, got %d (expected 200)", rec.Code)
	} else {
		t.Logf("/metrics returned %d (correctly public)", rec.Code)
	}
}

// mockRoutableProvider implements core.RoutableProvider for testing
type mockRoutableProvider struct{}

func (m *mockRoutableProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (m *mockRoutableProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockRoutableProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list", Data: []core.Model{}}, nil
}

func (m *mockRoutableProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (m *mockRoutableProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockRoutableProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (m *mockRoutableProvider) Route(model string) (core.Provider, error) {
	return nil, fmt.Errorf("no provider for model: %s", model)
}

func (m *mockRoutableProvider) Supports(model string) bool {
	return false
}

func (m *mockRoutableProvider) GetProviderType(model string) string {
	return ""
}

func (m *mockRoutableProvider) AllModels() []core.Model {
	return nil
}

// =============================================================================
// TEST 3: Circuit breaker race condition in half-open state
// =============================================================================

func TestCircuitBreakerHalfOpenRace(t *testing.T) {
	// This test verifies that in half-open state, only ONE probe request is allowed
	// through at a time to prevent thundering herd when recovering from failures.
	//
	// The circuit breaker design: after timeout in open state, allow exactly one
	// "probe" request through. If it succeeds, gradually close. If it fails, reopen.
	//
	// We use a longer timeout (1 second) so the circuit stays in half-open during our test
	cfg := llmclient.Config{
		ProviderName:   "test",
		BaseURL:        "http://localhost:9999", // Will fail immediately
		MaxRetries:     0,
		InitialBackoff: 1 * time.Millisecond,
		CircuitBreaker: &llmclient.CircuitBreakerConfig{
			FailureThreshold: 1,              // Open after 1 failure
			SuccessThreshold: 1,              // Close after 1 success
			Timeout:          1 * time.Second, // Long enough to keep half-open during test
		},
	}

	client := llmclient.New(cfg, nil)

	// Make one request to open the circuit
	ctx := context.Background()
	_, _ = client.DoRaw(ctx, llmclient.Request{
		Method:   "POST",
		Endpoint: "/test",
	})

	// Wait for circuit to transition to half-open (timeout must pass)
	time.Sleep(1100 * time.Millisecond)

	// Now test concurrent requests - only ONE should pass through half-open state
	var wg sync.WaitGroup
	passedThrough := atomic.Int64{}
	blockedByCircuit := atomic.Int64{}

	// All goroutines start at the same time
	start := make(chan struct{})

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // Wait for signal

			// Try to make request - only ONE should be allowed in half-open state
			_, err := client.DoRaw(ctx, llmclient.Request{
				Method:   "POST",
				Endpoint: "/test",
			})

			// Check if blocked by circuit breaker
			if err != nil {
				errMsg := err.Error()
				// Circuit breaker block message - check with contains since error format may vary
				if strings.Contains(errMsg, "circuit breaker is open") {
					blockedByCircuit.Add(1)
				} else {
					// Request got through circuit breaker but failed for other reason (connection refused, etc)
					passedThrough.Add(1)
					// Log first few errors to debug
					if passedThrough.Load() <= 3 {
						t.Logf("Error (not circuit breaker): %s", errMsg)
					}
				}
			} else {
				// Request succeeded (shouldn't happen with bad URL)
				passedThrough.Add(1)
			}
		}()
	}

	close(start) // Signal all goroutines to start
	wg.Wait()

	passed := passedThrough.Load()
	blocked := blockedByCircuit.Load()

	t.Logf("Requests passed through: %d, Blocked by circuit: %d", passed, blocked)

	// In half-open state, exactly ONE request should pass, rest should be blocked
	// After that one fails, circuit reopens and blocks all others
	if passed == 0 {
		t.Errorf("No requests passed through half-open circuit - circuit may be stuck")
	} else if passed > 1 {
		t.Errorf("POTENTIAL ISSUE: %d requests passed through half-open circuit (expected 1)", passed)
	} else {
		t.Logf("Circuit breaker working correctly: %d passed, %d blocked", passed, blocked)
	}
}

// =============================================================================
// TEST 4: Request body size limit (DoS vulnerability)
// =============================================================================

func TestRequestBodySizeLimit(t *testing.T) {
	mp := &mockRoutableProvider{}
	srv := server.New(mp, nil)

	// Create an 11MB request body (exceeds 10MB limit)
	largeBody := bytes.Repeat([]byte("x"), 11*1024*1024)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	// Should be rejected with 413 Request Entity Too Large
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Request body limit not enforced: expected 413, got %d", rec.Code)
		t.Logf("Server processed %d byte request", len(largeBody))
	} else {
		t.Logf("Request body limit enforced (got %d for %d byte body)", rec.Code, len(largeBody))
	}
}

// =============================================================================
// TEST 5: Empty cache startup behavior (documented design)
// =============================================================================

func TestEmptyCacheStartup(t *testing.T) {
	// This test documents the expected behavior on first startup with empty cache.
	// Per CLAUDE.md: "Registry loads from cache first, then refreshes in background"
	// "Server starts immediately with cached models while fresh data is fetched"
	// On first start with empty cache, requests will fail until background init completes.
	// This is intentional for non-blocking startup.

	registry := providers.NewModelRegistry()

	// Create mock provider with slow response
	mp := &mockProvider{
		models: []core.Model{{ID: "test-model"}},
		delay:  100 * time.Millisecond, // Slow initialization
	}

	registry.RegisterProviderWithType(mp, "mock")

	// Use a mock cache that returns empty
	mockCache := &emptyCache{}
	registry.SetCache(mockCache)

	// Start async init
	registry.InitializeAsync(context.Background())

	// Immediately try to get a provider
	provider := registry.GetProvider("test-model")

	if provider == nil {
		t.Log("Expected behavior: With empty cache, immediate requests return nil")
		t.Log("This is by design - non-blocking startup with background initialization")
	} else {
		t.Log("Provider found immediately (unexpected with empty cache)")
	}

	// Wait for init and try again
	time.Sleep(200 * time.Millisecond)
	provider = registry.GetProvider("test-model")

	if provider != nil {
		t.Log("Provider available after init completes")
	} else {
		t.Error("Provider still not available after init")
	}
}

type emptyCache struct{}

func (e *emptyCache) Get(ctx context.Context) (*modelcache.ModelCache, error) {
	return nil, nil
}

func (e *emptyCache) Set(ctx context.Context, cache *modelcache.ModelCache) error {
	return nil
}

func (e *emptyCache) Close() error {
	return nil
}

// =============================================================================
// TEST 6: Retry behavior on POST requests
// =============================================================================

func TestPostRequestsAreRetried(t *testing.T) {
	// For an LLM proxy, retrying POST requests on transient failures (502, 503, 504)
	// is desirable behavior. The upstream provider handles idempotency, and users
	// benefit from automatic retry rather than manual retry.

	requestCount := atomic.Int64{}

	// Create a test server that returns 503 (retryable)
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error": "temporarily unavailable"}`))
	}))
	defer testServer.Close()

	cfg := llmclient.Config{
		ProviderName:   "test",
		BaseURL:        testServer.URL,
		MaxRetries:     3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		BackoffFactor:  2.0,
	}

	client := llmclient.New(cfg, nil)

	ctx := context.Background()
	_, _ = client.DoRaw(ctx, llmclient.Request{
		Method:   "POST",
		Endpoint: "/v1/chat/completions",
		Body:     map[string]interface{}{"messages": []string{"hello"}},
	})

	count := requestCount.Load()
	expectedAttempts := int64(4) // 1 initial + 3 retries
	if count != expectedAttempts {
		t.Errorf("Expected %d attempts (1 + MaxRetries), got %d", expectedAttempts, count)
	} else {
		t.Logf("POST request correctly retried: %d total attempts", count)
	}
}

// =============================================================================
// TEST 7: Goroutine leak detection during streaming errors
// =============================================================================

// =============================================================================
// TEST 8: Unbounded buffer growth in stream converters
// =============================================================================

func TestUnboundedBufferGrowth(t *testing.T) {
	// Create a test server that sends very large SSE events
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		// Send a large SSE event (1MB of data in one event)
		largeData := bytes.Repeat([]byte("x"), 1024*1024)
		w.Write([]byte("data: "))
		w.Write(largeData)
		w.Write([]byte("\n\n"))
		flusher.Flush()

		// Send done signal
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer testServer.Close()

	cfg := llmclient.Config{
		ProviderName: "test",
		BaseURL:      testServer.URL,
		MaxRetries:   0,
	}

	client := llmclient.New(cfg, nil)

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	// Make multiple streaming requests with large events
	for i := 0; i < 5; i++ {
		ctx := context.Background()
		stream, err := client.DoStream(ctx, llmclient.Request{
			Method:   "POST",
			Endpoint: "/stream",
		})
		if err != nil {
			t.Logf("Stream request failed: %v", err)
			continue
		}

		// Read with small buffer (should cause internal buffer to grow)
		buf := make([]byte, 1024) // Small buffer
		totalRead := 0
		for {
			n, readErr := stream.Read(buf)
			totalRead += n
			if readErr != nil {
				break
			}
		}
		stream.Close()
		t.Logf("Read %d bytes from stream %d", totalRead, i)
	}

	runtime.GC()
	runtime.ReadMemStats(&memAfter)

	// Handle potential underflow from GC
	var memGrowth uint64
	if memAfter.HeapAlloc > memBefore.HeapAlloc {
		memGrowth = memAfter.HeapAlloc - memBefore.HeapAlloc
	} else {
		memGrowth = 0
	}

	if memGrowth > 10*1024*1024 { // More than 10MB growth
		t.Logf("WARNING: Significant memory growth detected: %d MB", memGrowth/(1024*1024))
		t.Log("This could indicate unbounded buffer growth vulnerability")
	} else {
		t.Logf("Memory growth: %d KB (acceptable)", memGrowth/1024)
	}
}

func TestStreamingGoroutineLeak(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	// Create a test server that closes connection mid-stream
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			w.Write([]byte("data: {\"partial\": true}\n\n"))
			flusher.Flush()
		}
		// Close connection abruptly (simulating network failure)
	}))
	defer testServer.Close()

	cfg := llmclient.Config{
		ProviderName: "test",
		BaseURL:      testServer.URL,
		MaxRetries:   0,
	}

	client := llmclient.New(cfg, nil)

	// Make multiple streaming requests that will fail
	for i := 0; i < 10; i++ {
		ctx := context.Background()
		stream, err := client.DoStream(ctx, llmclient.Request{
			Method:   "POST",
			Endpoint: "/stream",
		})
		if err == nil && stream != nil {
			// Read until error
			buf := make([]byte, 1024)
			for {
				_, readErr := stream.Read(buf)
				if readErr != nil {
					break
				}
			}
			stream.Close()
		}
	}

	// Give time for goroutines to clean up
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()
	leaked := finalGoroutines - initialGoroutines

	if leaked > 2 { // Allow some variance
		t.Errorf("WARNING: Potential goroutine leak detected: %d goroutines leaked", leaked)
		t.Logf("Initial: %d, Final: %d", initialGoroutines, finalGoroutines)
	} else {
		t.Logf("No significant goroutine leak detected (delta: %d)", leaked)
	}
}
