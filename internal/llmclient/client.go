// Package llmclient provides a base HTTP client for LLM providers with:
// - Request marshaling/unmarshaling
// - Retries with exponential backoff and jitter
// - Standardized error parsing (429, 502, 503, 504)
// - Circuit breaking with half-open state protection
package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/httpclient"
)

// RequestInfo contains metadata about a request for observability hooks
type RequestInfo struct {
	Provider string // Provider name (e.g., "openai", "anthropic")
	Model    string // Model name (e.g., "gpt-4", "claude-3-opus")
	Endpoint string // API endpoint (e.g., "/chat/completions", "/models")
	Method   string // HTTP method (e.g., "POST", "GET")
	Stream   bool   // Whether this is a streaming request
}

// ResponseInfo contains metadata about a response for observability hooks
type ResponseInfo struct {
	Provider   string        // Provider name
	Model      string        // Model name
	Endpoint   string        // API endpoint
	StatusCode int           // HTTP status code (0 if network error)
	Duration   time.Duration // Request duration
	Stream     bool          // Whether this was a streaming request
	Error      error         // Error if request failed (nil on success)
}

// Hooks defines observability callbacks for request lifecycle events.
// These hooks enable instrumentation without polluting business logic.
type Hooks struct {
	// OnRequestStart is called before a request is sent.
	// The returned context can be used to propagate trace spans or request IDs.
	OnRequestStart func(ctx context.Context, info RequestInfo) context.Context

	// OnRequestEnd is called after a request completes (success or failure).
	// For streaming requests, this is called when the stream starts, not when it closes.
	OnRequestEnd func(ctx context.Context, info ResponseInfo)
}

// Config holds configuration for the LLM client
type Config struct {
	// ProviderName is the identifier used in logs and metrics (e.g., "openai", "anthropic").
	ProviderName string
	// BaseURL is the base URL for the provider's API (e.g., "https://api.openai.com/v1").
	BaseURL string
	// Retry specifies retry behaviour for failed requests, including backoff and jitter settings.
	Retry config.RetryConfig
	// CircuitBreaker configures the circuit breaker that prevents cascading failures by
	// stopping requests to an unhealthy provider until it recovers.
	CircuitBreaker config.CircuitBreakerConfig
	// Hooks provides optional observability callbacks invoked on request start and end.
	Hooks Hooks
}

// DefaultConfig returns default client configuration
func DefaultConfig(providerName, baseURL string) Config {
	return Config{
		ProviderName:   providerName,
		BaseURL:        baseURL,
		Retry:          config.DefaultRetryConfig(),
		CircuitBreaker: config.DefaultCircuitBreakerConfig(),
	}
}

// HeaderSetter is a function that sets headers on an HTTP request
type HeaderSetter func(req *http.Request)

// Client is a base HTTP client for LLM providers
type Client struct {
	mu             sync.RWMutex
	httpClient     *http.Client
	config         Config
	headerSetter   HeaderSetter
	circuitBreaker *circuitBreaker
}

// New creates a new LLM client with the given configuration
func New(cfg Config, headerSetter HeaderSetter) *Client {
	c := &Client{
		httpClient:   httpclient.NewDefaultHTTPClient(),
		config:       cfg,
		headerSetter: headerSetter,
	}

	if cfg.CircuitBreaker.FailureThreshold > 0 {
		c.circuitBreaker = newCircuitBreaker(
			cfg.CircuitBreaker.FailureThreshold,
			cfg.CircuitBreaker.SuccessThreshold,
			cfg.CircuitBreaker.Timeout,
		)
	}

	return c
}

// NewWithHTTPClient creates a new LLM client with a custom HTTP client
func NewWithHTTPClient(httpClient *http.Client, cfg Config, headerSetter HeaderSetter) *Client {
	c := &Client{
		httpClient:   httpClient,
		config:       cfg,
		headerSetter: headerSetter,
	}

	if cfg.CircuitBreaker.FailureThreshold > 0 {
		c.circuitBreaker = newCircuitBreaker(
			cfg.CircuitBreaker.FailureThreshold,
			cfg.CircuitBreaker.SuccessThreshold,
			cfg.CircuitBreaker.Timeout,
		)
	}

	return c
}

// SetBaseURL updates the base URL (thread-safe)
func (c *Client) SetBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config.BaseURL = url
}

// BaseURL returns the current base URL (thread-safe)
func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.BaseURL
}

// getBaseURL returns the base URL for internal use (already holding lock or single-threaded)
func (c *Client) getBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.BaseURL
}

// Request represents an HTTP request to be made
type Request struct {
	Method   string
	Endpoint string
	Body     interface{} // Will be JSON marshaled if not nil
	RawBody  []byte      // Used as-is (e.g., multipart form bodies). Mutually exclusive with Body.
	Headers  map[string]string
}

// Response represents an HTTP response
type Response struct {
	StatusCode int
	Body       []byte
}

// Do executes a request with retries and circuit breaking, then unmarshals the response
func (c *Client) Do(ctx context.Context, req Request, result interface{}) error {
	resp, err := c.DoRaw(ctx, req)
	if err != nil {
		return err
	}

	if result != nil {
		if err := json.Unmarshal(resp.Body, result); err != nil {
			return core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "failed to unmarshal response: "+err.Error(), err)
		}
	}

	return nil
}

// DoRaw executes a request with retries and circuit breaking, returning the raw response.
//
// # Metrics Behavior
//
// Metrics hooks (OnRequestStart/OnRequestEnd) are called at this level to track logical
// requests from the caller's perspective, not individual retry attempts. This ensures:
//
//   - Request counts reflect user-facing requests, not internal HTTP calls
//   - Duration metrics include total time across all retries (useful for SLOs)
//   - In-flight gauge accurately reflects concurrent logical requests
//
// Behavior comparison (hooks at DoRaw vs per-attempt):
//
//	| Scenario                             | Per-attempt (old)           | DoRaw level (current)            |
//	|--------------------------------------|-----------------------------|----------------------------------|
//	| 1 request, succeeds first try        | 1 observation               | 1 observation                    |
//	| 1 request, fails twice then succeeds | 3 observations              | 1 observation (success)          |
//	| 1 request, fails all 3 retries       | 3 observations              | 1 observation (error)            |
//	| Duration metric                      | Each attempt's duration     | Total duration including retries |
//	| In-flight gauge                      | Bounces up/down per attempt | Accurate concurrent count        |
//
// The final status code and error in metrics reflect the outcome after all retry attempts.
func (c *Client) DoRaw(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()

	// Extract model for observability
	modelName := extractModel(req.Body)

	// Build request info for hooks
	reqInfo := RequestInfo{
		Provider: c.config.ProviderName,
		Model:    modelName,
		Endpoint: req.Endpoint,
		Method:   req.Method,
		Stream:   false,
	}

	// Call OnRequestStart hook (once per logical request, not per retry)
	if c.config.Hooks.OnRequestStart != nil {
		ctx = c.config.Hooks.OnRequestStart(ctx, reqInfo)
	}

	// Helper to call OnRequestEnd hook
	callEndHook := func(statusCode int, err error) {
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: statusCode,
				Duration:   time.Since(start),
				Stream:     false,
				Error:      err,
			})
		}
	}

	halfOpenProbe := false
	if c.circuitBreaker != nil {
		allowed, probe := c.circuitBreaker.acquire()
		if !allowed {
			err := core.NewProviderError(c.config.ProviderName, http.StatusServiceUnavailable,
				"circuit breaker is open - provider temporarily unavailable", nil)
			callEndHook(http.StatusServiceUnavailable, err)
			return nil, err
		}
		halfOpenProbe = probe
	}

	var lastErr error
	var lastStatusCode int
	maxAttempts := c.config.Retry.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Calculate backoff duration with jitter
			backoff := c.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				callEndHook(0, ctx.Err())
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := c.doRequest(ctx, req)
		if err != nil {
			lastErr = err
			lastStatusCode = extractStatusCode(err)
			// Only retry on network errors
			if c.circuitBreaker != nil {
				c.circuitBreaker.RecordFailure()
			}
			if halfOpenProbe {
				callEndHook(lastStatusCode, lastErr)
				return nil, lastErr
			}
			continue
		}

		// Check for retryable status codes
		if c.isRetryable(resp.StatusCode) {
			if c.circuitBreaker != nil {
				c.circuitBreaker.RecordFailure()
			}
			lastErr = core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil)
			lastStatusCode = resp.StatusCode
			if halfOpenProbe {
				callEndHook(lastStatusCode, lastErr)
				return nil, lastErr
			}
			continue
		}

		// Non-retryable error
		if resp.StatusCode != http.StatusOK {
			if c.circuitBreaker != nil {
				// Only record failure for server errors
				if resp.StatusCode >= 500 {
					c.circuitBreaker.RecordFailure()
				}
			}
			err := core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil)
			callEndHook(resp.StatusCode, err)
			return nil, err
		}

		// Success
		if c.circuitBreaker != nil {
			c.circuitBreaker.RecordSuccess()
		}
		callEndHook(resp.StatusCode, nil)
		return resp, nil
	}

	// All retries exhausted
	if lastErr != nil {
		callEndHook(lastStatusCode, lastErr)
		return nil, lastErr
	}
	err := core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "request failed after retries", nil)
	callEndHook(http.StatusBadGateway, err)
	return nil, err
}

// DoStream executes a streaming request, returning a ReadCloser
// Note: Streaming requests do NOT retry (as partial data may have been sent)
// Metrics note: Duration is measured from start to stream establishment, not stream close
func (c *Client) DoStream(ctx context.Context, req Request) (io.ReadCloser, error) {
	start := time.Now()

	// Extract model for observability
	modelName := extractModel(req.Body)

	// Build request info for hooks
	reqInfo := RequestInfo{
		Provider: c.config.ProviderName,
		Model:    modelName,
		Endpoint: req.Endpoint,
		Method:   req.Method,
		Stream:   true,
	}

	// Call OnRequestStart hook
	if c.config.Hooks.OnRequestStart != nil {
		ctx = c.config.Hooks.OnRequestStart(ctx, reqInfo)
	}

	// Check circuit breaker
	if c.circuitBreaker != nil && !c.circuitBreaker.Allow() {
		err := core.NewProviderError(c.config.ProviderName, http.StatusServiceUnavailable,
			"circuit breaker is open - provider temporarily unavailable", nil)
		// Call OnRequestEnd hook
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: http.StatusServiceUnavailable,
				Duration:   time.Since(start),
				Stream:     true,
				Error:      err,
			})
		}
		return nil, err
	}

	httpReq, err := c.buildRequest(ctx, req)
	if err != nil {
		// Call OnRequestEnd hook on error
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: extractStatusCode(err),
				Duration:   time.Since(start),
				Stream:     true,
				Error:      err,
			})
		}
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if c.circuitBreaker != nil {
			c.circuitBreaker.RecordFailure()
		}
		providerErr := core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "failed to send request: "+err.Error(), err)
		// Call OnRequestEnd hook on error
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: extractStatusCode(providerErr),
				Duration:   time.Since(start),
				Stream:     true,
				Error:      providerErr,
			})
		}
		return nil, providerErr
	}

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			respBody = []byte("failed to read error response")
		}
		_ = resp.Body.Close()

		if c.circuitBreaker != nil {
			if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
				c.circuitBreaker.RecordFailure()
			}
		}
		providerErr := core.ParseProviderError(c.config.ProviderName, resp.StatusCode, respBody, nil)
		// Call OnRequestEnd hook on error
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: resp.StatusCode,
				Duration:   time.Since(start),
				Stream:     true,
				Error:      providerErr,
			})
		}
		return nil, providerErr
	}

	if c.circuitBreaker != nil {
		c.circuitBreaker.RecordSuccess()
	}

	// Call OnRequestEnd hook on success (stream established)
	if c.config.Hooks.OnRequestEnd != nil {
		c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
			Provider:   c.config.ProviderName,
			Model:      modelName,
			Endpoint:   req.Endpoint,
			StatusCode: resp.StatusCode,
			Duration:   time.Since(start),
			Stream:     true,
			Error:      nil,
		})
	}

	return resp.Body, nil
}

// DoPassthrough executes a request and returns the raw upstream HTTP response.
// Unlike DoRaw, it preserves non-200 responses for the caller to proxy unchanged.
func (c *Client) DoPassthrough(ctx context.Context, req Request) (*http.Response, error) {
	start := time.Now()
	modelName := extractModel(req.Body)
	stream := strings.Contains(strings.ToLower(req.Headers["Accept"]), "text/event-stream")

	reqInfo := RequestInfo{
		Provider: c.config.ProviderName,
		Model:    modelName,
		Endpoint: req.Endpoint,
		Method:   req.Method,
		Stream:   stream,
	}
	if c.config.Hooks.OnRequestStart != nil {
		ctx = c.config.Hooks.OnRequestStart(ctx, reqInfo)
	}

	callEndHook := func(statusCode int, err error) {
		if c.config.Hooks.OnRequestEnd != nil {
			c.config.Hooks.OnRequestEnd(ctx, ResponseInfo{
				Provider:   c.config.ProviderName,
				Model:      modelName,
				Endpoint:   req.Endpoint,
				StatusCode: statusCode,
				Duration:   time.Since(start),
				Stream:     stream,
				Error:      err,
			})
		}
	}

	halfOpenProbe := false
	if c.circuitBreaker != nil {
		allowed, probe := c.circuitBreaker.acquire()
		if !allowed {
			err := core.NewProviderError(c.config.ProviderName, http.StatusServiceUnavailable,
				"circuit breaker is open - provider temporarily unavailable", nil)
			callEndHook(http.StatusServiceUnavailable, err)
			return nil, err
		}
		halfOpenProbe = probe
	}

	maxAttempts := c.config.Retry.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := c.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				callEndHook(0, ctx.Err())
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		httpReq, err := c.buildRequest(ctx, req)
		if err != nil {
			callEndHook(extractStatusCode(err), err)
			return nil, err
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			if c.circuitBreaker != nil {
				c.circuitBreaker.RecordFailure()
			}
			providerErr := core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "failed to send request: "+err.Error(), err)
			if halfOpenProbe || attempt == maxAttempts-1 {
				callEndHook(extractStatusCode(providerErr), providerErr)
				return nil, providerErr
			}
			continue
		}

		retryable := c.isRetryable(resp.StatusCode)
		if retryable {
			if c.circuitBreaker != nil {
				c.circuitBreaker.RecordFailure()
			}
			if halfOpenProbe || attempt == maxAttempts-1 {
				callEndHook(resp.StatusCode, nil)
				return resp, nil
			}
			_ = resp.Body.Close()
			continue
		}

		if c.circuitBreaker != nil {
			if resp.StatusCode >= 500 {
				c.circuitBreaker.RecordFailure()
			} else {
				c.circuitBreaker.RecordSuccess()
			}
		}

		callEndHook(resp.StatusCode, nil)
		return resp, nil
	}

	err := core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "request failed after retries", nil)
	callEndHook(http.StatusBadGateway, err)
	return nil, err
}

// extractModel attempts to extract the model name from a request body
func extractModel(body interface{}) string {
	if body == nil {
		return "unknown"
	}

	// Try ChatRequest
	if chatReq, ok := body.(*core.ChatRequest); ok && chatReq != nil {
		return chatReq.Model
	}

	// Try ResponsesRequest
	if respReq, ok := body.(*core.ResponsesRequest); ok && respReq != nil {
		return respReq.Model
	}

	// Unknown request type
	return "unknown"
}

// extractStatusCode tries to extract HTTP status code from an error
func extractStatusCode(err error) int {
	if err == nil {
		return 0
	}

	// Try to extract from GatewayError
	if gwErr, ok := err.(*core.GatewayError); ok {
		return gwErr.StatusCode
	}

	// Network or unknown error
	return 0
}

// doRequest executes a single HTTP request without retries.
// Note: Metrics hooks are called at the DoRaw level, not here, to avoid
// counting each retry attempt as a separate request.
func (c *Client) doRequest(ctx context.Context, req Request) (*Response, error) {
	httpReq, err := c.buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "failed to send request: "+err.Error(), err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "failed to read response: "+err.Error(), err)
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Body:       body,
	}, nil
}

// buildRequest creates an HTTP request from a Request
func (c *Client) buildRequest(ctx context.Context, req Request) (*http.Request, error) {
	// Validate request
	if req.Method == "" {
		return nil, core.NewInvalidRequestError("HTTP method is required", nil)
	}
	if req.Endpoint == "" {
		return nil, core.NewInvalidRequestError("endpoint is required", nil)
	}

	// Validate HTTP method
	switch req.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		// Valid methods
	default:
		return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid HTTP method: %s", req.Method), nil)
	}

	url := c.getBaseURL() + req.Endpoint

	var bodyReader io.Reader
	if req.Body != nil && req.RawBody != nil {
		return nil, core.NewInvalidRequestError("Body and RawBody cannot both be set", nil)
	}
	if req.RawBody != nil {
		bodyReader = bytes.NewReader(req.RawBody)
	} else if req.Body != nil {
		bodyBytes, err := json.Marshal(req.Body)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to marshal request", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to create request", err)
	}

	// Set default content type for requests with body
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Apply provider-specific headers
	if c.headerSetter != nil {
		c.headerSetter(httpReq)
	}

	// Apply request-specific headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	return httpReq, nil
}

// calculateBackoff calculates the backoff duration for a given attempt with jitter
func (c *Client) calculateBackoff(attempt int) time.Duration {
	retry := c.config.Retry
	backoff := float64(retry.InitialBackoff) * math.Pow(retry.BackoffFactor, float64(attempt-1))
	if backoff > float64(retry.MaxBackoff) {
		backoff = float64(retry.MaxBackoff)
	}

	if retry.JitterFactor > 0 {
		jitter := backoff * retry.JitterFactor
		//nolint:gosec // math/rand is fine for jitter, no crypto needed
		backoff = backoff - jitter + (rand.Float64() * 2 * jitter)
	}

	return time.Duration(backoff)
}

// isRetryable returns true if the status code indicates a retryable error
func (c *Client) isRetryable(statusCode int) bool {
	// Retry on rate limits and specific server errors that are typically transient
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusGatewayTimeout
}

// circuitBreaker implements a circuit breaker pattern with half-open state protection
type circuitBreaker struct {
	mu               sync.Mutex
	state            circuitState
	failures         int
	successes        int
	failureThreshold int
	successThreshold int
	timeout          time.Duration
	lastFailure      time.Time
	halfOpenAllowed  bool // Controls single-request probe in half-open state
}

type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

func newCircuitBreaker(failureThreshold, successThreshold int, timeout time.Duration) *circuitBreaker {
	return &circuitBreaker{
		state:            circuitClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
		halfOpenAllowed:  true,
	}
}

// acquire checks if a request should be allowed through the circuit breaker.
// The second return value reports whether the caller is the single half-open probe.
func (cb *circuitBreaker) acquire() (bool, bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return true, false
	case circuitOpen:
		// Check if timeout has passed
		if time.Since(cb.lastFailure) > cb.timeout {
			cb.state = circuitHalfOpen
			cb.successes = 0
			cb.halfOpenAllowed = true // Allow the first probe request
		} else {
			return false, false
		}
		// Fall through to half-open handling
		fallthrough
	case circuitHalfOpen:
		// Only allow one request through at a time in half-open state
		// This prevents thundering herd when transitioning from open
		if cb.halfOpenAllowed {
			cb.halfOpenAllowed = false
			return true, true
		}
		return false, false
	}
	return true, false
}

// Allow reports whether any request may proceed.
func (cb *circuitBreaker) Allow() bool {
	allowed, _ := cb.acquire()
	return allowed
}

// RecordSuccess records a successful request
func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitHalfOpen:
		cb.successes++
		cb.halfOpenAllowed = true // Allow next probe request
		if cb.successes >= cb.successThreshold {
			cb.state = circuitClosed
			cb.failures = 0
		}
	case circuitClosed:
		cb.failures = 0
	}
}

// RecordFailure records a failed request
func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case circuitClosed:
		if cb.failures >= cb.failureThreshold {
			cb.state = circuitOpen
		}
	case circuitHalfOpen:
		cb.state = circuitOpen
		cb.successes = 0
		cb.halfOpenAllowed = true // Reset for next timeout period
	}
}

// State returns the current circuit state (for testing/monitoring)
func (cb *circuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return "closed"
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half-open"
	}
	return "unknown"
}
