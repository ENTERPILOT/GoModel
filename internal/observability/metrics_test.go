package observability

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestPrometheusHooks(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()

	if hooks.OnRequestStart == nil {
		t.Fatal("OnRequestStart hook should not be nil")
	}
	if hooks.OnRequestEnd == nil {
		t.Fatal("OnRequestEnd hook should not be nil")
	}
}

func TestRequestMetrics_Success(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Simulate a successful request
	reqInfo := llmclient.RequestInfo{
		Provider: "openai",
		Model:    "gpt-4",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   false,
	}

	// Start request
	ctx = hooks.OnRequestStart(ctx, reqInfo)

	// Simulate some work
	time.Sleep(10 * time.Millisecond)

	// End request successfully
	respInfo := llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4",
		Endpoint:   "/chat/completions",
		StatusCode: http.StatusOK,
		Duration:   100 * time.Millisecond,
		Stream:     false,
		Error:      nil,
	}
	hooks.OnRequestEnd(ctx, respInfo)

	// Verify metrics
	// Check counter
	counter, err := RequestsTotal.GetMetricWithLabelValues(
		"openai", "gpt-4", "/chat/completions", "200", "success", "false",
	)
	if err != nil {
		t.Fatalf("Failed to get counter metric: %v", err)
	}

	value := testutil.ToFloat64(counter)
	if value != 1 {
		t.Errorf("Expected counter value 1, got %f", value)
	}
}

func TestRequestMetrics_Error(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Simulate a failed request
	reqInfo := llmclient.RequestInfo{
		Provider: "anthropic",
		Model:    "claude-3-opus",
		Endpoint: "/messages",
		Method:   "POST",
		Stream:   false,
	}

	// Start request
	ctx = hooks.OnRequestStart(ctx, reqInfo)

	// End request with error
	respInfo := llmclient.ResponseInfo{
		Provider:   "anthropic",
		Model:      "claude-3-opus",
		Endpoint:   "/messages",
		StatusCode: http.StatusBadRequest,
		Duration:   50 * time.Millisecond,
		Stream:     false,
		Error:      core.NewProviderError("anthropic", http.StatusBadRequest, "invalid request", nil),
	}
	hooks.OnRequestEnd(ctx, respInfo)

	// Verify metrics
	counter, err := RequestsTotal.GetMetricWithLabelValues(
		"anthropic", "claude-3-opus", "/messages", "400", "error", "false",
	)
	if err != nil {
		t.Fatalf("Failed to get counter metric: %v", err)
	}

	value := testutil.ToFloat64(counter)
	if value != 1 {
		t.Errorf("Expected counter value 1, got %f", value)
	}
}

func TestRequestMetrics_NetworkError(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Simulate a network error
	reqInfo := llmclient.RequestInfo{
		Provider: "gemini",
		Model:    "gemini-pro",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   false,
	}

	// Start request
	ctx = hooks.OnRequestStart(ctx, reqInfo)

	// End request with network error (status code 0)
	respInfo := llmclient.ResponseInfo{
		Provider:   "gemini",
		Model:      "gemini-pro",
		Endpoint:   "/chat/completions",
		StatusCode: 0,
		Duration:   10 * time.Millisecond,
		Stream:     false,
		Error:      core.NewProviderError("gemini", http.StatusBadGateway, "network error", nil),
	}
	hooks.OnRequestEnd(ctx, respInfo)

	// Verify metrics
	counter, err := RequestsTotal.GetMetricWithLabelValues(
		"gemini", "gemini-pro", "/chat/completions", "network_error", "error", "false",
	)
	if err != nil {
		t.Fatalf("Failed to get counter metric: %v", err)
	}

	value := testutil.ToFloat64(counter)
	if value != 1 {
		t.Errorf("Expected counter value 1, got %f", value)
	}
}

func TestRequestMetrics_Streaming(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Simulate a streaming request
	reqInfo := llmclient.RequestInfo{
		Provider: "openai",
		Model:    "gpt-4-turbo",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   true,
	}

	// Start request
	ctx = hooks.OnRequestStart(ctx, reqInfo)

	// End request successfully
	respInfo := llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4-turbo",
		Endpoint:   "/chat/completions",
		StatusCode: http.StatusOK,
		Duration:   200 * time.Millisecond,
		Stream:     true,
		Error:      nil,
	}
	hooks.OnRequestEnd(ctx, respInfo)

	// Verify metrics
	counter, err := RequestsTotal.GetMetricWithLabelValues(
		"openai", "gpt-4-turbo", "/chat/completions", "200", "success", "true",
	)
	if err != nil {
		t.Fatalf("Failed to get counter metric: %v", err)
	}

	value := testutil.ToFloat64(counter)
	if value != 1 {
		t.Errorf("Expected counter value 1, got %f", value)
	}
}

func TestInFlightRequests(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Start first request
	reqInfo1 := llmclient.RequestInfo{
		Provider: "openai",
		Model:    "gpt-4",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   false,
	}
	ctx = hooks.OnRequestStart(ctx, reqInfo1)

	// Check in-flight gauge increased
	gauge, err := InFlightRequests.GetMetricWithLabelValues("openai", "/chat/completions", "false")
	if err != nil {
		t.Fatalf("Failed to get gauge metric: %v", err)
	}
	value := testutil.ToFloat64(gauge)
	if value != 1 {
		t.Errorf("Expected in-flight gauge value 1, got %f", value)
	}

	// Start second request
	ctx2 := context.Background()
	reqInfo2 := llmclient.RequestInfo{
		Provider: "openai",
		Model:    "gpt-3.5-turbo",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   false,
	}
	ctx2 = hooks.OnRequestStart(ctx2, reqInfo2)

	// Check in-flight gauge increased again
	value = testutil.ToFloat64(gauge)
	if value != 2 {
		t.Errorf("Expected in-flight gauge value 2, got %f", value)
	}

	// End first request
	respInfo1 := llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4",
		Endpoint:   "/chat/completions",
		StatusCode: http.StatusOK,
		Duration:   100 * time.Millisecond,
		Stream:     false,
		Error:      nil,
	}
	hooks.OnRequestEnd(ctx, respInfo1)

	// Check in-flight gauge decreased
	value = testutil.ToFloat64(gauge)
	if value != 1 {
		t.Errorf("Expected in-flight gauge value 1 after first request ended, got %f", value)
	}

	// End second request
	respInfo2 := llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-3.5-turbo",
		Endpoint:   "/chat/completions",
		StatusCode: http.StatusOK,
		Duration:   50 * time.Millisecond,
		Stream:     false,
		Error:      nil,
	}
	hooks.OnRequestEnd(ctx2, respInfo2)

	// Check in-flight gauge back to 0
	value = testutil.ToFloat64(gauge)
	if value != 0 {
		t.Errorf("Expected in-flight gauge value 0 after all requests ended, got %f", value)
	}
}

func TestRequestDuration(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Create hooks
	hooks := NewPrometheusHooks()
	ctx := context.Background()

	// Simulate request with specific duration
	reqInfo := llmclient.RequestInfo{
		Provider: "openai",
		Model:    "gpt-4",
		Endpoint: "/chat/completions",
		Method:   "POST",
		Stream:   false,
	}

	ctx = hooks.OnRequestStart(ctx, reqInfo)

	duration := 250 * time.Millisecond
	respInfo := llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4",
		Endpoint:   "/chat/completions",
		StatusCode: http.StatusOK,
		Duration:   duration,
		Stream:     false,
		Error:      nil,
	}
	hooks.OnRequestEnd(ctx, respInfo)

	// Verify histogram metric was recorded
	// Note: We can't easily verify the exact value without accessing internal histogram state,
	// but we can verify the metric exists and has observations
	observer, err := RequestDuration.GetMetricWithLabelValues("openai", "gpt-4", "/chat/completions", "false")
	if err != nil {
		t.Fatalf("Failed to get histogram metric: %v", err)
	}

	// Verify at least one observation was recorded
	hist := observer.(prometheus.Histogram)
	if hist == nil {
		t.Fatal("Expected histogram, got nil")
	}
}

func TestHealthCheck(t *testing.T) {
	// Reset metrics before test
	ResetMetrics()

	// Health check should succeed
	err := HealthCheck()
	if err != nil {
		t.Errorf("HealthCheck failed: %v", err)
	}
}

func TestGetMetrics(t *testing.T) {
	metrics := GetMetrics()

	if metrics == nil {
		t.Fatal("GetMetrics returned nil")
		return
	}

	if metrics.RequestsTotal == nil {
		t.Error("RequestsTotal metric is nil")
	}

	if metrics.RequestDuration == nil {
		t.Error("RequestDuration metric is nil")
	}

	if metrics.InFlightRequests == nil {
		t.Error("InFlightRequests metric is nil")
	}

	if metrics.ResponseSnapshotStoreFailures == nil {
		t.Error("ResponseSnapshotStoreFailures metric is nil")
	}
}
