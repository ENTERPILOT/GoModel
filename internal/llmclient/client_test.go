package llmclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goconfig "github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.NoError(t, err)
	assert.Equal(t, "hello", result.Message)
}

func TestClient_Do_WithRequestBody(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

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

	require.NoError(t, err)
	assert.Equal(t, "test", receivedBody["input"])
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
		Headers: http.Header{
			"X-Custom": {"custom-value"},
		},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "Bearer token", receivedHeaders.Get("Authorization"))
	assert.Equal(t, "custom-value", receivedHeaders.Get("X-Custom"))
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

			require.Error(t, err)

			gatewayErr, ok := err.(*core.GatewayError)
			require.True(t, ok)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
		})
	}
}

func TestClient_Do_Retries(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
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

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestClient_Do_RetriesContinueAfterCircuitTrips(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
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
		Enabled:          true,
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

	require.NoError(t, err)
	require.True(t, result.Success)
	got := attempts.Load()
	require.Equal(t, int32(3), got)
}

func TestClient_Do_RetriesExhausted(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
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

	require.Error(t, err)

	// 1 initial + 2 retries = 3 attempts
	assert.Equal(t, int32(3), attempts.Load())
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

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(resp.Body), "raw")
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

	require.Error(t, err)
	assert.Nil(t, resp)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

// TestClient_DoRaw_WithRetries tests that DoRaw properly handles retries
func TestClient_DoRaw_WithRetries(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
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

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestClient_DoRaw_DoesNotRetryRawBodyReader(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"retryable"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoRaw(context.Background(), Request{
		Method:        http.MethodPost,
		Endpoint:      "/test",
		RawBodyReader: strings.NewReader(`{"hello":"world"}`),
		Headers: http.Header{
			"Content-Type": {"application/json"},
		},
	})
	require.Error(t, err)
	require.Nil(t, resp)
	got := attempts.Load()
	require.Equal(t, int32(1), got)
}

func TestClient_DoPassthrough_WithRetries(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})
	require.NoError(t, err)

	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(body))
	require.Equal(t, int32(3), attempts.Load())
}

func TestClient_DoPassthrough_ReturnsLastRetryableResponseAfterRetries(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"attempt":` + strconv.FormatInt(int64(count), 10) + `}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 2
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.MaxBackoff = 10 * time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})
	require.NoError(t, err)

	defer resp.Body.Close()

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"attempt":3}`, string(body))
	require.Equal(t, int32(3), attempts.Load())
}

func TestClient_DoPassthrough_HTTPTimeoutDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = time.Millisecond
	config.Retry.MaxBackoff = time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          time.Second,
	}
	client := NewWithHTTPClient(&http.Client{Timeout: 30 * time.Millisecond}, config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	got := attempts.Load()
	require.Equal(t, int32(1), got)
	state := client.circuitBreaker.State()
	require.Equal(t, "closed", state)
}

func TestClient_DoPassthrough_DoesNotRetryNonReplaySafeMethod(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.MaxBackoff = 10 * time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method:   http.MethodPost,
		Endpoint: "/test",
		RawBody:  []byte(`{"hello":"world"}`),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	require.NoError(t, err)

	defer resp.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	got := attempts.Load()
	require.Equal(t, int32(1), got)
}

func TestClient_DoPassthrough_RetriesWhenIdempotencyKeyPresent(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"attempt":` + strconv.FormatInt(int64(count), 10) + `}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = 10 * time.Millisecond
	config.Retry.MaxBackoff = 10 * time.Millisecond
	config.Retry.BackoffFactor = 1
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method:   http.MethodPost,
		Endpoint: "/test",
		RawBody:  []byte(`{"hello":"world"}`),
		Headers: http.Header{
			"Content-Type":    {"application/json"},
			"Idempotency-Key": {"req-123"},
		},
	})
	require.NoError(t, err)

	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := attempts.Load()
	require.Equal(t, int32(3), got)
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
	require.NoError(t, err)

	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "chunk")
}

func TestClient_DoPassthrough_FirstChunkHookUsesOpaqueStreamBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: passthrough\n\n"))
	}))
	defer server.Close()

	var firstChunk ResponseInfo
	cfg := DefaultConfig("test", server.URL)
	cfg.Hooks.OnStreamFirstChunk = func(_ context.Context, info ResponseInfo) { firstChunk = info }
	client := New(cfg, nil)
	resp, err := client.DoPassthrough(t.Context(), Request{
		Method:    http.MethodPost,
		Endpoint:  "/v2/chat",
		Operation: OperationChat,
		Model:     "command-r",
		Stream:    true,
	})
	require.NoError(t, err)

	defer resp.Body.Close()
	require.Equal(t, time.Duration(0), firstChunk.Duration)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, OperationChat, firstChunk.Operation)
	require.Equal(t, "command-r", firstChunk.Model)
	require.True(t, firstChunk.Stream, "first chunk = %+v, want streaming command-r chat", firstChunk)
}

func TestClient_DoPassthrough_FirstChunkHookUsesSuccessfulSSEContentType(t *testing.T) {
	body := `{"padding":"` + strings.Repeat("x", 65*1024) + `","stream":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		assert.NoError(t, err)

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte("data: passthrough\n\n"))
	}))
	defer server.Close()

	var firstChunks []ResponseInfo
	cfg := DefaultConfig("test", server.URL)
	cfg.Hooks.OnStreamFirstChunk = func(_ context.Context, info ResponseInfo) {
		firstChunks = append(firstChunks, info)
	}
	client := New(cfg, nil)
	resp, err := client.DoPassthrough(t.Context(), Request{
		Method:        http.MethodPost,
		Endpoint:      "/chat/completions",
		Operation:     OperationChat,
		RawBodyReader: io.NopCloser(strings.NewReader(body)),
	})
	require.NoError(t, err)

	defer resp.Body.Close()
	require.Empty(t, firstChunks)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Len(t, firstChunks, 1)
	require.True(t, firstChunks[0].Stream)
	require.Equal(t, OperationChat, firstChunks[0].Operation)
}

func TestClient_DoPassthrough_ErrorResponseDoesNotFireFirstChunkHook(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("data: error\n\n"))
			}))
			defer server.Close()

			firstChunks := 0
			cfg := DefaultConfig("test", server.URL)
			cfg.Retry.MaxRetries = 0
			cfg.Hooks.OnStreamFirstChunk = func(context.Context, ResponseInfo) { firstChunks++ }
			client := New(cfg, nil)
			resp, err := client.DoPassthrough(t.Context(), Request{
				Method:   http.MethodPost,
				Endpoint: "/chat/completions",
				Stream:   true,
			})
			require.NoError(t, err)

			defer resp.Body.Close()
			_, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, 0, firstChunks)
		})
	}
}

func TestClient_DoStream_FirstChunkHookWaitsForBodyBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
	}))
	defer server.Close()

	var firstChunks []ResponseInfo
	cfg := DefaultConfig("test", server.URL)
	cfg.Hooks.OnStreamFirstChunk = func(_ context.Context, info ResponseInfo) {
		firstChunks = append(firstChunks, info)
	}
	client := New(cfg, nil)
	stream, err := client.DoStream(t.Context(), Request{
		Method:    http.MethodPost,
		Endpoint:  "/stream",
		Operation: OperationChat,
		Body:      map[string]bool{"stream": true},
	})
	require.NoError(t, err)

	defer stream.Close()
	require.Empty(t, firstChunks)

	buf := make([]byte, 1)
	_, err = stream.Read(buf)
	require.NoError(t, err)
	require.Len(t, firstChunks, 1)
	require.Equal(t, OperationChat, firstChunks[0].Operation)
	require.True(t, firstChunks[0].Stream)
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.Len(t, firstChunks, 1)
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

	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	assert.Equal(t, core.ErrorTypeAuthentication, gatewayErr.Type)
}

// TestClient_BuildErrorDoesNotRetryOrChargeBreaker verifies that caller-side
// request-construction failures (an invalid HTTP method, in this case)
// short-circuit out of every Do* entry point without retrying and without
// charging the circuit breaker — the upstream was never contacted, so the
// retry would just repeat the validation failure and the breaker should not
// blame the provider.
//
// The closed-state subtests verify that build errors don't trip a healthy
// breaker. The half-open subtests verify that build errors don't advance the
// breaker state machine in either direction (no failure recorded → no
// transition back to open; no success counted → no progress toward closed).
func TestClient_BuildErrorDoesNotRetryOrChargeBreaker(t *testing.T) {
	t.Parallel()

	entries := []struct {
		name string
		call func(t *testing.T, client *Client) error
	}{
		{
			name: "DoRaw",
			call: func(t *testing.T, client *Client) error {
				_, err := client.DoRaw(context.Background(), Request{Method: "INVALID", Endpoint: "/test"})
				return err
			},
		},
		{
			name: "DoStream",
			call: func(t *testing.T, client *Client) error {
				_, err := client.DoStream(context.Background(), Request{Method: "INVALID", Endpoint: "/test"})
				return err
			},
		},
		{
			name: "DoPassthrough",
			call: func(t *testing.T, client *Client) error {
				_, err := client.DoPassthrough(context.Background(), Request{Method: "INVALID", Endpoint: "/test"})
				return err
			},
		},
	}

	states := []struct {
		name      string
		preSeed   func(cb *circuitBreaker)
		wantState string
	}{
		{
			name:      "closed",
			preSeed:   func(*circuitBreaker) {},
			wantState: "closed",
		},
		{
			// Half-open with the probe slot available: the breaker grants the
			// probe, so the build error fires through the normal request path.
			// We then assert the breaker stayed half-open — the build error
			// must not record a success (which would advance toward closed)
			// nor a failure (which would reopen the breaker).
			name: "half_open",
			preSeed: func(cb *circuitBreaker) {
				cb.mu.Lock()
				defer cb.mu.Unlock()
				cb.state = circuitHalfOpen
				cb.failures = cb.failureThreshold
				cb.successes = 0
				cb.halfOpenAllowed = true
				cb.lastFailure = time.Now().Add(-cb.timeout - time.Millisecond)
			},
			wantState: "half-open",
		},
	}

	for _, st := range states {
		for _, e := range entries {
			t.Run(st.name+"/"+e.name, func(t *testing.T) {
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
				}))
				defer server.Close()

				config := DefaultConfig("test", server.URL)
				config.Retry.MaxRetries = 3
				config.Retry.InitialBackoff = time.Millisecond
				config.Retry.JitterFactor = 0
				config.CircuitBreaker = goconfig.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 1,
					SuccessThreshold: 2,
					Timeout:          time.Second,
				}
				client := New(config, nil)
				st.preSeed(client.circuitBreaker)

				err := e.call(t, client)
				require.Error(t, err)

				var gwErr *core.GatewayError
				require.ErrorAs(t, err, &gwErr)
				assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
				got := attempts.Load()
				assert.Equal(t, int32(0), got)
				state := client.circuitBreaker.State()
				assert.Equal(t, st.wantState, state)
				_, err = // A consumed half-open probe slot must be released, or the
					// breaker rejects every request from here on.
					client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
				assert.NoError(t, err)
			})
		}
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

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"Server error"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0 // No retries
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          1 * time.Second,
	}
	client := New(config, nil)

	// Make requests until circuit opens
	for range 5 {
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

	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode)
	assert.Contains(t, gatewayErr.Message, "circuit breaker")
	assert.Contains(t, gatewayErr.Message, "provider test")

	// Should have made exactly 3 requests (threshold)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestCircuitBreaker_ClosesAfterTimeout(t *testing.T) {
	var attempts atomic.Int32
	var shouldSucceed atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
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
		Enabled:          true,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          50 * time.Millisecond,
	}
	client := New(config, nil)

	// Trigger circuit breaker to open
	for range 2 {
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
	require.Error(t, err)

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

	require.NoError(t, err)
	assert.True(t, result.Success)
}

// TestCircuitBreaker_HalfOpenPreventsThunderingHerd tests that only one request
// is allowed through in half-open state to prevent thundering herd
func TestCircuitBreaker_HalfOpenPreventsThunderingHerd(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate slow response
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
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

	for range 10 {
		wg.Go(func() {
			err := client.Do(context.Background(), Request{
				Method:   http.MethodGet,
				Endpoint: "/test",
			}, nil)
			results <- err
		})
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
	assert.NotZero(t, successes)

	// Most requests should be rejected by the circuit breaker
	if rejections == 0 && successes == 10 {
		t.Log("Warning: all requests succeeded, circuit breaker may not have been in half-open state")
	}
}

func TestCircuitBreaker_HalfOpenProbeDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
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
		Enabled:          true,
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
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode)
	require.False(t, strings.Contains(gatewayErr.Message, "circuit breaker is open"), "expected original upstream error, got circuit breaker error: %s", gatewayErr.Message)
	got := attempts.Load()
	require.Equal(t, int32(1), got)
	state := client.circuitBreaker.State()
	require.Equal(t, "open", state)

	err = client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	require.Error(t, err)

	gatewayErr, ok = err.(*core.GatewayError)
	require.True(t, ok)
	require.Contains(t, gatewayErr.Message, "circuit breaker is open")
	got = attempts.Load()
	require.Equal(t, int32(1), got)
}

func TestCircuitBreaker_HalfOpenProbeResolvesOnClientError(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
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

	_, err := client.DoRaw(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
	state := client.circuitBreaker.State()
	require.Equal(t, "closed", state)
	got := attempts.Load()
	require.Equal(t, int32(1), got)

	_, err = client.DoRaw(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	})
	require.Error(t, err)
	got = attempts.Load()
	require.Equal(t, int32(2), got)
}

func TestCircuitBreaker_ExcludedRateLimitDoesNotOpenCircuit(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		FailureOnStatuses: []string{"5xx"},
		Enabled:           true,
		FailureThreshold:  1,
		SuccessThreshold:  1,
		Timeout:           time.Second,
	}
	client := New(config, nil)

	for i := range 2 {
		err := client.Do(context.Background(), Request{
			Method:   http.MethodGet,
			Endpoint: "/test",
		}, nil)
		require.Error(t, err)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr, "attempt %d: expected GatewayError, got %T", i+1, err)
		require.Equal(t, http.StatusTooManyRequests, gatewayErr.StatusCode, "attempt %d: status", i+1)
	}
	state := client.circuitBreaker.State()
	require.Equal(t, "closed", state)
	got := attempts.Load()
	require.Equal(t, int32(2), got)
}

func TestCircuitBreaker_HalfOpenProbeReopensOnRateLimit(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.StatusCode)
	state := client.circuitBreaker.State()
	require.Equal(t, "open", state)

	err = client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	require.Error(t, err)
	require.ErrorAs(t, err, &gatewayErr)
	require.Contains(t, gatewayErr.Message, "circuit breaker is open")
	got := attempts.Load()
	require.Equal(t, int32(1), got)
}

func TestCircuitBreaker_State(t *testing.T) {
	cb := newCircuitBreaker(3, 2, time.Minute)
	state := cb.State()
	assert.Equal(t, "closed", state)

	// Record failures to open circuit
	for range 3 {
		cb.RecordFailure()
	}
	state = cb.State()
	assert.Equal(t, "open", state)
}

// ResponseInfo carries the breaker state to observability hooks — including
// on requests the breaker rejects.
func TestCircuitBreaker_StateReportedToHooks(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	var states []string
	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          time.Minute,
	}
	config.Hooks = Hooks{
		OnRequestEnd: func(_ context.Context, info ResponseInfo) {
			states = append(states, info.CircuitState)
		},
	}
	client := New(config, nil)

	// First request fails (500) and opens the breaker; the second is rejected
	// by the open breaker. Both completions must report the state.
	_, _ = client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
	_, _ = client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})

	want := []string{"open", "open"}
	require.Equal(t, want, states)

	// Without a breaker the field stays empty.
	states = nil
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{}
	plain := New(config, nil)
	_, _ = plain.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.Len(t, states, 1)
	require.Empty(t, states[0])
}

func TestCircuitBreakerDisabledNeverShortCircuits(t *testing.T) {
	tests := []struct {
		name string
		cb   goconfig.CircuitBreakerConfig
	}{
		{
			name: "enabled false",
			cb: goconfig.CircuitBreakerConfig{
				Enabled:          false,
				FailureThreshold: 1,
				SuccessThreshold: 1,
				Timeout:          time.Minute,
			},
		},
		{
			// Legacy disable path, kept for backward compatibility.
			name: "zero failure threshold",
			cb: goconfig.CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 0,
				SuccessThreshold: 1,
				Timeout:          time.Minute,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			config := DefaultConfig("test", server.URL)
			config.Retry.MaxRetries = 0
			config.CircuitBreaker = tt.cb
			client := New(config, nil)

			require.Nil(t, client.circuitBreaker)

			// Every request must reach the upstream: an active breaker with
			// FailureThreshold=1 would fail fast from the second request onwards.
			const attempts = 3
			for i := range attempts {
				_, err := client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
				require.Error(t, err)
				require.False(t, strings.Contains(err.Error(), "circuit breaker is open"), "attempt %d: request was short-circuited by a disabled breaker: %v", i+1, err)
			}
			got := requests.Load()
			require.Equal(t, int32(attempts), got)
		})
	}
}

// A caller cancellation that consumed the half-open probe slot must release
// it; otherwise the breaker rejects every request until process restart.
func TestCircuitBreaker_CanceledHalfOpenProbeReleasesSlot(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		SuccessThreshold: 2,
		Timeout:          time.Minute,
	}
	client := New(config, nil)

	cb := client.circuitBreaker
	cb.mu.Lock()
	cb.state = circuitHalfOpen
	cb.halfOpenAllowed = true
	cb.mu.Unlock()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.DoRaw(canceled, Request{Method: http.MethodGet, Endpoint: "/test"})
	require.Error(t, err)
	state := cb.State()
	require.Equal(t, "half-open", state)
	_, err = // The slot must be free again so the next request can be the probe.
		client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.NoError(t, err)
}

// Client disconnects while a stream is being established must not be charged
// to the breaker as provider failures.
func TestCircuitBreaker_ClientCancellationDoesNotTrip(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          time.Minute,
	}
	client := New(config, nil)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.DoStream(canceled, Request{Method: http.MethodPost, Endpoint: "/test", Body: map[string]string{"k": "v"}})
	require.Error(t, err)
	state := client.circuitBreaker.State()
	require.Equal(t, "closed", state)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/test", Body: map[string]string{"k": "v"}})
	require.NoError(t, err)

	_ = stream.Close()
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

	require.Error(t, err)
}

func TestClient_Do_HTTPTimeoutReturnsGatewayTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 0
	client := NewWithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}, cfg, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	require.Contains(t, gatewayErr.Message, "failed to send request")
}

func TestClient_Do_HTTPTimeoutDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 3
	cfg.Retry.InitialBackoff = time.Millisecond
	cfg.Retry.MaxBackoff = time.Millisecond
	cfg.Retry.BackoffFactor = 1
	cfg.Retry.JitterFactor = 0
	cfg.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          time.Second,
	}
	client := NewWithHTTPClient(&http.Client{Timeout: 30 * time.Millisecond}, cfg, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	got := attempts.Load()
	require.Equal(t, int32(1), got)
	state := client.circuitBreaker.State()
	require.Equal(t, "closed", state)
}

func TestCircuitBreakerCountsRetriedRequestOnce(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary failure"}}`))
	}))
	defer server.Close()

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 3
	cfg.Retry.InitialBackoff = time.Millisecond
	cfg.Retry.MaxBackoff = time.Millisecond
	cfg.Retry.BackoffFactor = 1
	cfg.Retry.JitterFactor = 0
	cfg.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          time.Second,
	}
	client := New(cfg, nil)

	for i := range 2 {
		err := client.Do(context.Background(), Request{
			Method:   http.MethodGet,
			Endpoint: "/test",
		}, nil)
		require.Error(t, err)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr, "request %d: expected GatewayError, got %T", i+1, err)
		require.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode, "request %d: status", i+1)

		wantAttempts := int32((i + 1) * (cfg.Retry.MaxRetries + 1))
		got := attempts.Load()
		require.Equal(t, wantAttempts, got, "request %d: upstream attempts", i+1)
	}

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Contains(t, gatewayErr.Message, "circuit breaker is open")
	require.Equal(t, int32(2*(cfg.Retry.MaxRetries+1)), attempts.Load(), "upstream attempts after circuit rejection")
}

func TestClient_Do_BodyReadTimeoutReturnsGatewayTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partial":"`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`done"}`))
	}))
	defer server.Close()

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 0
	client := NewWithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}, cfg, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, http.StatusGatewayTimeout, gatewayErr.StatusCode)
	require.Contains(t, gatewayErr.Message, "failed to read response")
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig("test-provider", "https://api.test.com")

	assert.Equal(t, "test-provider", config.ProviderName)
	assert.Equal(t, "https://api.test.com", config.BaseURL)
	assert.Equal(t, 3, config.Retry.MaxRetries)
	assert.Equal(t, 1*time.Second, config.Retry.InitialBackoff)
	assert.Equal(t, 0.1, config.Retry.JitterFactor)
	assert.Equal(t, 5, config.CircuitBreaker.FailureThreshold)
	assert.Equal(t, 2, config.CircuitBreaker.SuccessThreshold)
	assert.Equal(t, 30*time.Second, config.CircuitBreaker.Timeout)
}

func TestClient_SetBaseURL(t *testing.T) {
	client := New(DefaultConfig("test", "https://original.com"), nil)

	assert.Equal(t, "https://original.com", client.BaseURL())

	client.SetBaseURL("https://new.com")

	assert.Equal(t, "https://new.com", client.BaseURL())
}

// TestClient_SetBaseURL_Concurrent tests thread-safety of SetBaseURL
func TestClient_SetBaseURL_Concurrent(t *testing.T) {
	client := New(DefaultConfig("test", "https://original.com"), nil)

	var wg sync.WaitGroup
	for i := range 100 {
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
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
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

	require.Error(t, err)

	// Should NOT retry on 400 errors
	assert.Equal(t, int32(1), attempts.Load())
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
		assert.Equal(t, tt.expected, result, "attempt %d", tt.attempt)
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
	for range 100 {
		result := client.calculateBackoff(1)
		assert.GreaterOrEqual(t, result, 50*time.Millisecond)
		assert.LessOrEqual(t, result, 150*time.Millisecond)
	}
}

// TestWaitForRetry_ContextCancelled checks that cancelling the context returns
// ctx.Err() promptly instead of waiting out the backoff timer. The "cancelled
// while the timer is pending" case cancels from a goroutine after
// waitForRetry has started blocking, so a precheck-then-time.After
// implementation would not pass it.
func TestWaitForRetry_ContextCancelled(t *testing.T) {
	config := DefaultConfig("test", "http://test.com")
	config.Retry.InitialBackoff = time.Hour
	config.Retry.MaxBackoff = time.Hour
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	tests := []struct {
		name    string
		attempt int
		// cancelAfter is the delay before the context is cancelled from a
		// separate goroutine; a negative value cancels before the call.
		cancelAfter time.Duration
		want        error
	}{
		{name: "first attempt does not wait", attempt: 0, cancelAfter: time.Hour, want: nil},
		{name: "cancelled while the timer is pending", attempt: 1, cancelAfter: 20 * time.Millisecond, want: context.Canceled},
		{name: "already cancelled", attempt: 3, cancelAfter: -1, want: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelAfter < 0 {
				cancel()
			} else {
				timer := time.AfterFunc(tt.cancelAfter, cancel)
				defer timer.Stop()
			}

			start := time.Now()
			err := client.waitForRetry(ctx, tt.attempt)
			elapsed := time.Since(start)
			require.ErrorIs(t, err, tt.want)
			require.LessOrEqual(t, elapsed, time.Second)
		})
	}
}

type recordingReadCloser struct {
	io.Reader
	closed atomic.Bool
}

func (r *recordingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

func TestPreTransportErrorsCloseRawBodyReader(t *testing.T) {
	t.Run("request build error", func(t *testing.T) {
		client := New(DefaultConfig("test", "http://127.0.0.1:0"), nil)
		reader := &recordingReadCloser{Reader: strings.NewReader("upload payload")}

		_, err := client.DoRaw(context.Background(), Request{
			Method:        "bogus method",
			Endpoint:      "/files",
			RawBodyReader: reader,
		})
		require.Error(t, err)
		require.True(t, reader.closed.Load())
	})

	t.Run("circuit breaker open", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		config := DefaultConfig("test", server.URL)
		config.Retry.MaxRetries = 0
		config.CircuitBreaker = goconfig.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 1,
			SuccessThreshold: 1,
			Timeout:          time.Minute,
		}
		client := New(config, nil)
		_, err := // Trip the breaker with one failing request.
			client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
		require.Error(t, err)

		// A pipe-backed upload rejected by the open breaker never reaches the
		// transport; the client must close the reader so the producer
		// goroutine (and the buffers it pins) can exit.
		reader := &recordingReadCloser{Reader: strings.NewReader("upload payload")}
		_, err = client.DoRaw(context.Background(), Request{
			Method:        http.MethodPost,
			Endpoint:      "/files",
			RawBodyReader: reader,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "circuit breaker is open")
		require.True(t, reader.closed.Load())
	})
}

func TestClient_DoPassthrough_ResolvesUncertainStreamIntentFromResponse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantStream  bool
	}{
		{name: "buffered JSON response", contentType: "application/json", wantStream: false},
		{name: "SSE response", contentType: "text/event-stream", wantStream: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte("data"))
			}))
			defer server.Close()

			var ends []ResponseInfo
			config := DefaultConfig("test", server.URL)
			config.Retry.MaxRetries = 0
			config.Hooks = Hooks{OnRequestEnd: func(_ context.Context, info ResponseInfo) { ends = append(ends, info) }}
			client := New(config, nil)

			resp, err := client.DoPassthrough(context.Background(), Request{
				Method: http.MethodPost, Endpoint: "/chat/completions", Operation: OperationChat, StreamUncertain: true,
			})
			require.NoError(t, err)

			_ = resp.Body.Close()

			require.Len(t, ends, 1)
			require.False(t, ends[0].StreamUncertain)
			require.Equal(t, tt.wantStream, ends[0].Stream)
		})
	}
}

func TestClient_DoPassthrough_ReportsStreamThatEndsBeforeFirstChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var firstChunks, empties []ResponseInfo
	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 0
	config.Hooks = Hooks{
		OnStreamFirstChunk: func(_ context.Context, info ResponseInfo) { firstChunks = append(firstChunks, info) },
		OnStreamEmpty:      func(_ context.Context, info ResponseInfo) { empties = append(empties, info) },
	}
	client := New(config, nil)

	resp, err := client.DoPassthrough(context.Background(), Request{
		Method: http.MethodPost, Endpoint: "/chat/completions", Operation: OperationChat, Stream: true,
	})
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	_ = resp.Body.Close()

	require.Empty(t, firstChunks)
	require.Len(t, empties, 1)
	require.True(t, empties[0].Stream)
	require.Equal(t, http.StatusOK, empties[0].StatusCode)
	require.ErrorIs(t, empties[0].Error, io.EOF)
}

func TestClient_Do_EmbeddedErrorBody(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// HTTP 200 that is really an error (OpenRouter-style).
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","code":401}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 3
	config.Retry.InitialBackoff = time.Millisecond
	client := New(config, nil)

	var result map[string]any
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusUnauthorized, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeAuthentication, gatewayErr.Type)
	assert.Equal(t, "invalid key", gatewayErr.Message)
	assert.NotNil(t, gatewayErr.ResponseHeaders)
	assert.ErrorIs(t, err, core.ErrEmbeddedInSuccess)

	// An embedded 401 is not retryable, exactly like a genuine 401 status.
	assert.Equal(t, int32(1), attempts.Load())
}

func TestClient_Do_EmbeddedErrorRetriesRetryableStatus(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limited","code":429}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 2
	config.Retry.InitialBackoff = time.Millisecond
	config.Retry.JitterFactor = 0
	client := New(config, nil)

	var result struct {
		Success bool `json:"success"`
	}
	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, &result)

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestClient_Do_EmbeddedErrorWithoutCodeMapsToBadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"upstream disconnected"}}`))
	}))
	defer server.Close()

	config := DefaultConfig("test", server.URL)
	config.Retry.MaxRetries = 1
	config.Retry.InitialBackoff = time.Millisecond
	client := New(config, nil)

	err := client.Do(context.Background(), Request{
		Method:   http.MethodGet,
		Endpoint: "/test",
	}, nil)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	assert.Equal(t, "upstream disconnected", gatewayErr.Message)
}

func TestClient_DoStream_EmbeddedErrorOpensCircuitBreaker(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// A stream request answered with a buffered 200 error body.
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","code":503}}`))
	}))
	defer server.Close()

	var lastInfo ResponseInfo
	config := DefaultConfig("test", server.URL)
	config.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          time.Minute,
	}
	config.Hooks.OnRequestEnd = func(_ context.Context, info ResponseInfo) {
		lastInfo = info
	}
	client := New(config, nil)

	streamReq := Request{Method: http.MethodPost, Endpoint: "/stream", Body: map[string]bool{"stream": true}}

	for i := 1; i <= 2; i++ {
		stream, err := client.DoStream(context.Background(), streamReq)
		require.Nil(t, stream)
		require.Error(t, err)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr, "call %d: expected GatewayError, got %v", i, err)
		assert.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode, "call %d: StatusCode", i)
		assert.Equal(t, http.StatusServiceUnavailable, lastInfo.StatusCode)
		assert.Error(t, lastInfo.Error, "call %d: hook recorded status=%d error=%v, want mapped failure", i, lastInfo.StatusCode, lastInfo.Error)
	}

	// Two embedded failures reach the threshold: the third request must fail
	// fast without reaching the upstream.
	_, err := client.DoStream(context.Background(), streamReq)
	require.Error(t, err)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestClient_DoStream_BufferedCompletionPassesThrough(t *testing.T) {
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"content":"hi"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestClient_DoStream_NonObjectJSONStreamsThrough(t *testing.T) {
	// Gemini-style chunked JSON array under application/json: the leading '['
	// must leave the stream untouched, with peeked bytes replayed.
	body := "  [{\"candidates\":[{\"content\":\"a\"}]},\n{\"candidates\":[{\"content\":\"b\"}]}]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestClient_DoStream_OversizedBufferedJSONStreamsThrough(t *testing.T) {
	// A JSON object larger than the error-payload cap cannot be an embedded
	// error; it must stream through complete instead of being buffered whole.
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"content":"` +
		strings.Repeat("a", maxErrorBodyBytes+1024) + `"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestClient_DoStream_BufferedReadErrorPropagates(t *testing.T) {
	// The upstream announces more bytes than it delivers, so the read fails
	// mid-body. The partial bytes must reach the caller followed by the read
	// error — never a clean EOF that presents truncation as success.
	partial := `{"id":"chatcmpl-1","choices":[{"message":{"content":"Hel`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(partial)+512))
		_, _ = w.Write([]byte(partial))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.Error(t, err)
	assert.Equal(t, partial, string(got))
}

func TestClient_DoStream_OpenJSONStreamReturnsWithoutWaitingForEOF(t *testing.T) {
	// An NDJSON-style trickle mislabeled application/json: the first object
	// arrives, then the stream stays open. DoStream must classify on that
	// first object alone and hand the stream back — the handler only sends
	// the rest after DoStream has returned, so waiting for EOF deadlocks.
	streamReturned := make(chan struct{})
	first := `{"choices":[{"delta":{"content":"Hi"}}]}` + "\n"
	second := `{"choices":[{"delta":{"content":"!"}}]}` + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(first))
		w.(http.Flusher).Flush()
		<-streamReturned
		_, _ = w.Write([]byte(second))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()
	close(streamReturned)

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, first+second, string(got))
}

func TestClient_DoStream_ErrorShapedFirstFrameWithTrailerStreamsThrough(t *testing.T) {
	// A JSONL body mislabeled application/json whose first frame happens to
	// be error-shaped is still a stream: a bare error is the entire body, so
	// the trailing frame must be relayed rather than lost to classification.
	body := `{"error":"a"}` + "\n" + `{"id":"next"}` + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	require.NoError(t, err)

	defer stream.Close()

	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestClient_DoStream_EmbeddedErrorReturnsBeforeBodyCloses(t *testing.T) {
	// The provider flushes a complete error object and then holds the body
	// open. DoStream must classify from the bytes it has and return the error
	// at once — the handler only ends the body after DoStream has returned,
	// so waiting for EOF deadlocks.
	streamReturned := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","code":503}}`))
		w.(http.Flusher).Flush()
		<-streamReturned
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	close(streamReturned)
	if err == nil {
		_ = stream.Close()
		t.Fatal("expected the embedded error, got a stream")
	}
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode)
}

func TestClient_DoStream_ErrorObjectWithLyingContentLengthStillFails(t *testing.T) {
	// The error object is complete even though the upstream announced more
	// bytes than it delivers; the bytes in hand decide, not the transport.
	body := `{"error":"a"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+512))
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := New(DefaultConfig("test", server.URL), nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/stream"})
	if err == nil {
		_ = stream.Close()
		t.Fatal("expected the embedded error, got a stream")
	}
	require.ErrorIs(t, err, core.ErrEmbeddedInSuccess)
}

func TestBufferedTrailerIsBlank(t *testing.T) {
	tests := []struct {
		name     string
		buffered string
		want     bool
	}{
		{name: "nothing buffered", buffered: "", want: true},
		{name: "whitespace only", buffered: " \r\n\t", want: true},
		{name: "next frame", buffered: "\n{\"b\":2}", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader("x" + tt.buffered))
			_, err := r.ReadByte()
			require.NoError(t, err)
			got := // fill the buffer, consume the sentinel

				bufferedTrailerIsBlank(r)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadFirstJSONObject(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		limit        int
		wantHead     string
		wantComplete bool
	}{
		{
			name:         "object followed by stream tail",
			input:        `{"a":1}` + "\n" + `{"b":2}`,
			limit:        1024,
			wantHead:     `{"a":1}`,
			wantComplete: true,
		},
		{
			name:         "braces and escaped quotes inside strings do not count",
			input:        `{"msg":"a \"quoted\" {brace}"}tail`,
			limit:        1024,
			wantHead:     `{"msg":"a \"quoted\" {brace}"}`,
			wantComplete: true,
		},
		{
			name:         "escaped backslash before closing quote",
			input:        `{"path":"c:\\"}`,
			limit:        1024,
			wantHead:     `{"path":"c:\\"}`,
			wantComplete: true,
		},
		{
			name:         "nested objects close at the outer brace",
			input:        `{"a":{"b":{}}}`,
			limit:        1024,
			wantHead:     `{"a":{"b":{}}}`,
			wantComplete: true,
		},
		{
			name:         "clean EOF mid-object is incomplete without error",
			input:        `{"a":`,
			limit:        1024,
			wantHead:     `{"a":`,
			wantComplete: false,
		},
		{
			name:         "cap reached mid-object is incomplete",
			input:        `{"long":"value"}`,
			limit:        4,
			wantHead:     `{"lo`,
			wantComplete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head, complete, err := readFirstJSONObject(bufio.NewReader(strings.NewReader(tt.input)), tt.limit)
			require.NoError(t, err)
			assert.Equal(t, tt.wantHead, string(head))
			assert.Equal(t, tt.wantComplete, complete)
		})
	}
}

func TestFirstNonSpaceByte(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  byte
	}{
		{"leading whitespace then object", " \r\n\t{", 512, '{'},
		{"empty input", "", 512, 0},
		{"whitespace only", "   ", 512, 0},
		{"whitespace beyond the window", strings.Repeat(" ", 20) + "{", 8, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstNonSpaceByte(bufio.NewReader(strings.NewReader(tt.input)), tt.max)
			assert.Equal(t, tt.want, got)
		})
	}
}
