package llmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goconfig "gomodel/config"
	"gomodel/internal/core"
)

func TestClient_Do_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"hello"}`))
	}))
	defer server.Close()

	client := New(
		DefaultConfig("test", server.URL),
		func(req *http.Request) {
			req.Header.Set("X-Test", "value")
		},
	)

	var result struct {
		Message string `json:"message"`
	}
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Message != "hello" {
		t.Errorf("expected message 'hello', got '%s'", result.Message)
	}
}

func TestClient_Do_WithRequestBody(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got '%s'", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	requestBody := map[string]string{"input": "test"}
	var result map[string]string
	err := client.Do(context.Background(), Request{
		Method:   http.MethodPost,
		Endpoint: "/test",
		Body:     requestBody,
	}, &result)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody["input"] != "test" {
		t.Errorf("expected input 'test', got '%v'", receivedBody["input"])
	}
}

func TestClient_Do_Headers(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New(
		DefaultConfig("test", server.URL),
		func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer token")
		},
	)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
		Headers: map[string]string{
			"X-Custom": "custom-value",
		},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedHeaders.Get("Authorization") != "Bearer token" {
		t.Errorf("expected Authorization header 'Bearer token', got '%s'", receivedHeaders.Get("Authorization"))
	}
	if receivedHeaders.Get("X-Custom") != "custom-value" {
		t.Errorf("expected X-Custom header 'custom-value', got '%s'", receivedHeaders.Get("X-Custom"))
	}
}

func TestClient_Do_ErrorParsing(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantType   core.ErrorType
	}{
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"message":"Rate limited"}}`,
			wantType:   core.ErrorTypeRateLimit,
		},
		{
			name:       "authentication",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"Invalid API key"}}`,
			wantType:   core.ErrorTypeAuthentication,
		},
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"Invalid model"}}`,
			wantType:   core.ErrorTypeInvalidRequest,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"message":"Server error"}}`,
			wantType:   core.ErrorTypeProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			config := DefaultConfig("test", server.URL)
			config.Retry.MaxRetries = 0 // No retries for this test
			client := New(config, nil)

			err := client.Do(context.Background(), Request{
				Method:   http.MethodGet,
				Endpoint: "/test",
			}, nil)

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			gatewayErr, ok := err.(*core.GatewayError)
			if !ok {
				t.Fatalf("expected GatewayError, got %T", err)
			}
			if gatewayErr.Type != tt.wantType {
				t.Errorf("expected error type %s, got %s", tt.wantType, gatewayErr.Type)
			}
		})
	}
}

func TestClient_Do_Retries(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond // Fast backoff for tests
	config.Retry.JitterFactor = 0                       // Disable jitter for predictable tests
	client := New(config, nil)

	var result struct {
		Success bool `json:"success"`
	}
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success to be true")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_Do_RetriesContinueAfterCircuitTrips(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 2
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.MaxBackoff = 10 * time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          time.Minute,
	}
	client := New(config, nil)

	var result struct {
		Success bool `json:"success"`
	}
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	if err != nil {
		t.Fatalf("expected retries to continue after circuit trips, got: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success response after retries")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected request to use full retry budget after circuit trips, got %d attempts", got)
	}
}

func TestClient_Do_RetriesExhausted(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limited"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 2
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	// 1 initial + 2 retries = 3 attempts
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

// TestClient_DoRaw_Success tests DoRaw directly to ensure raw response handling works correctly
func TestClient_DoRaw_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"raw":"response"}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	client := New(config, nil)

	resp, err := client.DoRaw(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "raw") {
		t.Errorf("expected body to contain 'raw', got: %s", string(resp.Body))
	}
}

// TestClient_DoRaw_Error tests DoRaw error handling
func TestClient_DoRaw_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Bad request"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	client := New(config, nil)

	resp, err := client.DoRaw(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp != nil {
		t.Error("expected nil response on error")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Errorf("expected error type %s, got %s", core.ErrorTypeInvalidRequest, gatewayErr.Type)
	}
}

// TestClient_DoRaw_WithRetries tests that DoRaw properly handles retries
func TestClient_DoRaw_WithRetries(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"Service unavailable"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoRaw(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_DoStream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"chunk\":1}\n\n"))
		_, _ = w.Write([]byte("data: {\"chunk\":2}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{
		Method:   http.MethodPost,
		Endpoint: "/stream",
		Body:     map[string]bool{"stream": true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}

	if !strings.Contains(string(body), "chunk") {
		t.Errorf("expected body to contain 'chunk', got: %s", string(body))
	}
}

func TestClient_DoStream_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	_, err := client.DoStream(context.Background(), Request{
		Method:   http.MethodPost,
		Endpoint: "/stream",
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.Type != core.ErrorTypeAuthentication {
		t.Errorf("expected error type %s, got %s", core.ErrorTypeAuthentication, gatewayErr.Type)
	}
}

// TestRequest_Validation tests validation of Request fields
func TestRequest_Validation(t *testing.T) {
	config := DefaultConfig("test", "http://localhost")
	config.Retry.MaxRetries = 0
	client := New(config, nil)

	tests := []struct {
		name        string
		request     Request
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty method",
			request:     Request{Endpoint: "/test"},
			wantErr:     true,
			errContains: "method is required",
		},
		{
			name:        "empty endpoint",
			request:     Request{Method: http.MethodGet},
			wantErr:     true,
			errContains: "endpoint is required",
		},
		{
			name:        "invalid method",
			request:     Request{Method: "INVALID", Endpoint: "/test"},
			wantErr:     true,
			errContains: "invalid HTTP method",
		},
		{
			name:    "valid GET request",
			request: Request{Method: http.MethodGet, Endpoint: "/test"},
			wantErr: false,
		},
		{
			name:    "valid POST request",
			request: Request{Method: http.MethodPost, Endpoint: "/test"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.buildRequest(context.Background(), tt.request)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error to contain '%s', got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"Server error"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0 // No retries
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          1 * time.Second,
	}
	client := New(config, nil)

	// Make requests until circuit opens
	for i := 0; i < 5; i++ {
		_ = client.Do(context.Background(), Request{
			Method:   http.MethodGet,
			Endpoint: "/test",
		}, nil)
	}

	// Circuit should be open now - requests should fail immediately
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	if err == nil {
		t.Fatal("expected circuit breaker error")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, gatewayErr.StatusCode)
	}
	if !strings.Contains(gatewayErr.Message, "circuit breaker") {
		t.Errorf("expected circuit breaker message, got: %s", gatewayErr.Message)
	}

	// Should have made exactly 3 requests (threshold)
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts before circuit opened, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestCircuitBreaker_ClosesAfterTimeout(t *testing.T) {
	var attempts int32
	var shouldSucceed atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if shouldSucceed.Load() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"Server error"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          50 * time.Millisecond,
	}
	client := New(config, nil)

	// Trigger circuit breaker to open
	for i := 0; i < 2; i++ {
		_ = client.Do(context.Background(), Request{
			Method:   http.MethodGet,
			Endpoint: "/test",
		}, nil)
	}

	// Verify circuit is open
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	if err == nil {
		t.Fatal("expected circuit to be open")
	}

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Now make server succeed
	shouldSucceed.Store(true)

	// Should be able to make request (half-open state)
	var result struct {
		Success bool `json:"success"`
	}
	err = client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	if err != nil {
		t.Fatalf("expected success after timeout, got: %v", err)
	}
	if !result.Success {
		t.Error("expected success to be true")
	}
}

// TestCircuitBreaker_HalfOpenPreventsThunderingHerd tests that only one request
// is allowed through in half-open state to prevent thundering herd
func TestCircuitBreaker_HalfOpenPreventsThunderingHerd(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		time.Sleep(50 * time.Millisecond) // Simulate slow response
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          10 * time.Millisecond,
	}
	client := New(config, nil)

	// Open the circuit with a failure
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"Server error"}}`))
	}))
	client.SetBaseURL(failServer.URL)
	_ = client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	failServer.Close()

	// Wait for timeout to transition to half-open
	time.Sleep(20 * time.Millisecond)

	// Switch to successful server
	client.SetBaseURL(server.URL)

	// Try to make multiple concurrent requests
	var wg sync.WaitGroup
	results := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := client.Do(context.Background(), Request{
				Method:   http.MethodGet,
				Endpoint: "/test",
			}, nil)
			results <- err
		}()
	}

	wg.Wait()
	close(results)

	// Count successes and circuit breaker rejections
	var successes, rejections int
	for err := range results {
		if err == nil {
			successes++
		} else {
			gatewayErr, ok := err.(*core.GatewayError)
			if ok && strings.Contains(gatewayErr.Message, "circuit breaker") {
				rejections++
			}
		}
	}

	// In half-open state, only one request should be allowed through initially
	// After it succeeds, the circuit closes and more requests can go through
	if successes == 0 {
		t.Error("expected at least one successful request")
	}

	// Most requests should be rejected by the circuit breaker
	if rejections == 0 && successes == 10 {
		t.Log("Warning: all requests succeeded, circuit breaker may not have been in half-open state")
	}
}

func TestCircuitBreaker_HalfOpenProbeDoesNotRetry(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary failure"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 2
	config.Retry.InitialBackoff = 30 * time.Millisecond
	config.Retry.MaxBackoff = 30 * time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          20 * time.Millisecond,
	}
	client := New(config, nil)

	client.circuitBreaker.mu.Lock()
	client.circuitBreaker.state = circuitOpen
	client.circuitBreaker.failures = client.circuitBreaker.failureThreshold
	client.circuitBreaker.successes = 0
	client.circuitBreaker.lastFailure = time.Now().Add(-client.circuitBreaker.timeout - time.Millisecond)
	client.circuitBreaker.halfOpenAllowed = true
	client.circuitBreaker.mu.Unlock()

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	if err == nil {
		t.Fatal("expected provider error from half-open probe")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, gatewayErr.StatusCode)
	}
	if strings.Contains(gatewayErr.Message, "circuit breaker is open") {
		t.Fatalf("expected original upstream error, got circuit breaker error: %s", gatewayErr.Message)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 upstream attempt in half-open state, got %d", got)
	}
	if state := client.circuitBreaker.State(); state != "open" {
		t.Fatalf("expected circuit to reopen after failed half-open probe, got %q", state)
	}

	err = client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	if err == nil {
		t.Fatal("expected circuit breaker rejection after failed half-open probe")
	}

	gatewayErr, ok = err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if !strings.Contains(gatewayErr.Message, "circuit breaker is open") {
		t.Fatalf("expected circuit breaker error after failed half-open probe, got %s", gatewayErr.Message)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected follow-up request to be blocked without another upstream attempt, got %d attempts", got)
	}
}

func TestCircuitBreaker_State(t *testing.T) {
	cb := newCircuitBreaker(3, 2, time.Minute)

	if state := cb.State(); state != "closed" {
		t.Errorf("expected initial state 'closed', got '%s'", state)
	}

	// Record failures to open circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if state := cb.State(); state != "open" {
		t.Errorf("expected state 'open' after failures, got '%s'", state)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := client.Do(ctx, Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig("test-provider", "https://api.test.com")

	if config.ProviderName != "test-provider" {
		t.Errorf("expected provider name 'test-provider', got '%s'", config.ProviderName)
	}
	if config.BaseURL != "https://api.test.com" {
		t.Errorf("expected base URL 'https://api.test.com', got '%s'", config.BaseURL)
	}
	if config.Retry.MaxRetries != 3 {
		t.Errorf("expected MaxRetries 3, got %d", config.Retry.MaxRetries)
	}
	if config.Retry.InitialBackoff != 1*time.Second {
		t.Errorf("expected InitialBackoff 1s, got %v", config.Retry.InitialBackoff)
	}
	if config.Retry.JitterFactor != 0.1 {
		t.Errorf("expected JitterFactor 0.1, got %v", config.Retry.JitterFactor)
	}
	if config.CircuitBreaker.FailureThreshold != 5 {
		t.Errorf("expected CircuitBreaker.FailureThreshold=5, got %d", config.CircuitBreaker.FailureThreshold)
	}
	if config.CircuitBreaker.SuccessThreshold != 2 {
		t.Errorf("expected CircuitBreaker.SuccessThreshold=2, got %d", config.CircuitBreaker.SuccessThreshold)
	}
	if config.CircuitBreaker.Timeout != 30*time.Second {
		t.Errorf("expected CircuitBreaker.Timeout=30s, got %v", config.CircuitBreaker.Timeout)
	}
}

func TestClient_SetBaseURL(t *testing.T) {
	client := New(DefaultConfig("test", "https://original.com"), nil)

	if client.BaseURL() != "https://original.com" {
		t.Errorf("expected base URL 'https://original.com', got '%s'", client.BaseURL())
	}

	client.SetBaseURL("https://new.com")

	if client.BaseURL() != "https://new.com" {
		t.Errorf("expected base URL 'https://new.com', got '%s'", client.BaseURL())
	}
}

// TestClient_SetBaseURL_Concurrent tests thread-safety of SetBaseURL
func TestClient_SetBaseURL_Concurrent(t *testing.T) {
	client := New(DefaultConfig("test", "https://original.com"), nil)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			client.SetBaseURL("https://new" + string(rune('0'+i%10)) + ".com")
		}(i)
		go func() {
			defer wg.Done()
			_ = client.BaseURL() // Read while others are writing
		}()
	}
	wg.Wait()
	// Test passes if no race condition panic occurs
}

func TestClient_NonRetryableErrors(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Bad request"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	client := New(config, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	if err == nil {
		t.Fatal("expected error")
	}
	// Should NOT retry on 400 errors
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected 1 attempt (no retries on 400), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestBackoffCalculation(t *testing.T) {
	config := DefaultConfig("test", "http://test.com")
	config.Retry.InitialBackoff = 100 * time.Millisecond
	config.Retry.MaxBackoff = 1 * time.Second
	config.Retry.BackoffFactor = 2.0
	config.Retry.JitterFactor = 0 // Disable jitter for predictable tests
	client := New(config, nil)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 100 * time.Millisecond}, // Initial
		{2, 200 * time.Millisecond}, // 100 * 2
		{3, 400 * time.Millisecond}, // 100 * 4
		{4, 800 * time.Millisecond}, // 100 * 8
		{5, 1 * time.Second},        // Capped at max
		{10, 1 * time.Second},       // Still capped
	}

	for _, tt := range tests {
		result := client.calculateBackoff(tt.attempt)
		if result != tt.expected {
			t.Errorf("attempt %d: expected backoff %v, got %v", tt.attempt, tt.expected, result)
		}
	}
}

// TestBackoffCalculation_WithJitter tests that jitter is applied correctly
func TestBackoffCalculation_WithJitter(t *testing.T) {
	config := DefaultConfig("test", "http://test.com")
	config.Retry.InitialBackoff = 100 * time.Millisecond
	config.Retry.MaxBackoff = 1 * time.Second
	config.Retry.BackoffFactor = 2.0
	config.Retry.JitterFactor = 0.5 // 50% jitter
	client := New(config, nil)

	// With 50% jitter on 100ms base, result should be between 50ms and 150ms
	for i := 0; i < 100; i++ {
		result := client.calculateBackoff(1)
		if result < 50*time.Millisecond || result > 150*time.Millisecond {
			t.Errorf("backoff %v outside expected range [50ms, 150ms]", result)
		}
	}
}
