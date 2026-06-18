// Package llmclient provides a base HTTP client for LLM providers with:
// - Request marshaling/unmarshaling
// - Retries with exponential backoff and jitter
// - Standardized error parsing (429, 502, 503, 504)
// - Circuit breaking with half-open state protection
package llmclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

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
	Body     any    // Will be JSON marshaled if not nil
	RawBody  []byte // Used as-is (e.g., multipart form bodies). Mutually exclusive with Body and RawBodyReader.
	// RawBodyReader streams the request body without buffering it in memory.
	// It is intended for one-shot passthrough requests and is not replayable for retries.
	RawBodyReader io.Reader
	Headers       http.Header
}

// Response represents an HTTP response
type Response struct {
	StatusCode int
	// ContentType is the upstream response Content-Type header, preserved so
	// callers can describe the bytes actually returned (e.g. audio formats).
	// Only this header is surfaced; the full upstream header set is not exposed.
	ContentType string
	Body        []byte
}

type requestScope struct {
	ctx           context.Context
	startedAt     time.Time
	requestInfo   RequestInfo
	halfOpenProbe bool
}

func (c *Client) beginRequest(ctx context.Context, req Request, stream bool) (requestScope, error) {
	scope := requestScope{
		ctx:       ctx,
		startedAt: time.Now(),
		requestInfo: RequestInfo{
			Provider: c.config.ProviderName,
			Model:    extractModel(req.Body),
			Endpoint: req.Endpoint,
			Method:   req.Method,
			Stream:   stream,
		},
	}

	if c.config.Hooks.OnRequestStart != nil {
		scope.ctx = c.config.Hooks.OnRequestStart(scope.ctx, scope.requestInfo)
	}

	if c.circuitBreaker != nil {
		allowed, probe := c.circuitBreaker.acquire()
		if !allowed {
			err := core.NewProviderError(c.config.ProviderName, http.StatusServiceUnavailable,
				"circuit breaker is open - provider temporarily unavailable", nil)
			c.finishRequest(scope, http.StatusServiceUnavailable, err)
			return requestScope{}, err
		}
		scope.halfOpenProbe = probe
	}

	return scope, nil
}

func (c *Client) finishRequest(scope requestScope, statusCode int, err error) {
	if c.config.Hooks.OnRequestEnd == nil {
		return
	}
	c.config.Hooks.OnRequestEnd(scope.ctx, ResponseInfo{
		Provider:   c.config.ProviderName,
		Model:      scope.requestInfo.Model,
		Endpoint:   scope.requestInfo.Endpoint,
		StatusCode: statusCode,
		Duration:   time.Since(scope.startedAt),
		Stream:     scope.requestInfo.Stream,
		Error:      err,
	})
}

// completeScope is the standard terminal step for a request that has passed
// beginRequest. It records the circuit-breaker outcome (using cbErr to decide
// whether the failure was transport-level) and emits the metrics observation.
// Use this whenever a code path returns from one of the public Do* methods.
func (c *Client) completeScope(scope requestScope, statusCode int, err, cbErr error) {
	c.recordCircuitBreakerCompletion(statusCode, cbErr)
	c.finishRequest(scope, statusCode, err)
}

// failAfterRetries handles the "exhausted retries with no captured error"
// fallback shared by the retrying entry points (DoRaw, DoPassthrough). The
// returned error is also reported through the scope.
func (c *Client) failAfterRetries(scope requestScope) error {
	err := core.NewProviderError(c.config.ProviderName, http.StatusBadGateway, "request failed after retries", nil)
	c.completeScope(scope, http.StatusBadGateway, err, err)
	return err
}

// waitForRetryAttempt sleeps for the per-attempt backoff (a no-op for
// attempt 0) and finalises the scope if the context cancels mid-wait. The
// caller should return early when this returns a non-nil error.
func (c *Client) waitForRetryAttempt(ctx context.Context, scope requestScope, attempt int) error {
	if err := c.waitForRetry(ctx, attempt); err != nil {
		c.finishRequest(scope, 0, err)
		return err
	}
	return nil
}

func (c *Client) recordCircuitBreakerCompletion(statusCode int, err error) {
	if c.circuitBreaker == nil {
		return
	}
	if err != nil {
		c.circuitBreaker.RecordFailure()
		return
	}
	if statusCode == http.StatusTooManyRequests {
		if c.circuitBreaker.IsHalfOpen() {
			c.circuitBreaker.RecordFailure()
		}
		return
	}
	if c.shouldTripCircuitBreaker(statusCode) {
		c.circuitBreaker.RecordFailure()
		return
	}
	c.circuitBreaker.RecordSuccess()
}

func (c *Client) shouldTripCircuitBreaker(statusCode int) bool {
	if statusCode == http.StatusTooManyRequests {
		return false
	}
	return c.isRetryable(statusCode) || statusCode >= http.StatusInternalServerError
}

func (c *Client) maxAttempts() int {
	maxAttempts := c.config.Retry.MaxRetries + 1
	if maxAttempts < 1 {
		return 1
	}
	return maxAttempts
}

func (c *Client) waitForRetry(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	backoff := c.calculateBackoff(attempt)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
		return nil
	}
}

// Do executes a request with retries and circuit breaking, then unmarshals the response
func (c *Client) Do(ctx context.Context, req Request, result any) error {
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
	scope, err := c.beginRequest(ctx, req, false)
	if err != nil {
		return nil, err
	}
	ctx = scope.ctx

	var lastErr error
	var lastStatusCode int
	lastErrFromTransport := false
	maxAttempts := c.maxAttempts()
	if req.RawBodyReader != nil {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := c.waitForRetryAttempt(ctx, scope, attempt); err != nil {
			return nil, err
		}

		resp, err := c.doRequest(ctx, req)
		if err != nil {
			lastErr = err
			lastStatusCode = extractStatusCode(err)
			// Caller-side build errors (validation, body conflicts, marshal
			// failures) will repeat deterministically and never reached the
			// upstream — short-circuit without retrying and skip the breaker
			// entirely (a 400 with cbErr=nil would otherwise be recorded as
			// a success by recordCircuitBreakerCompletion).
			if isLocalRequestBuildError(err) {
				c.finishRequest(scope, lastStatusCode, err)
				return nil, err
			}
			lastErrFromTransport = true
			// Client-side timeouts are already the caller's latency budget. Do
			// not retry them, or the logical request can outlive HTTP_TIMEOUT.
			if scope.halfOpenProbe || isClientTimeoutGatewayError(lastErr) {
				c.completeScope(scope, lastStatusCode, lastErr, lastErr)
				return nil, lastErr
			}
			continue
		}

		// Check for retryable status codes
		if c.isRetryable(resp.StatusCode) {
			lastErr = core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil)
			lastStatusCode = resp.StatusCode
			lastErrFromTransport = false
			if scope.halfOpenProbe {
				c.completeScope(scope, lastStatusCode, lastErr, nil)
				return nil, lastErr
			}
			continue
		}

		// Non-retryable error
		if resp.StatusCode != http.StatusOK {
			parsedErr := core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil)
			c.completeScope(scope, resp.StatusCode, parsedErr, nil)
			return nil, parsedErr
		}

		// Success
		c.completeScope(scope, resp.StatusCode, nil, nil)
		return resp, nil
	}

	// All retries exhausted
	if lastErr != nil {
		var circuitErr error
		if lastErrFromTransport {
			circuitErr = lastErr
		}
		c.completeScope(scope, lastStatusCode, lastErr, circuitErr)
		return nil, lastErr
	}
	return nil, c.failAfterRetries(scope)
}

// DoStream executes a streaming request, returning a ReadCloser
// Note: Streaming requests do NOT retry (as partial data may have been sent)
// Metrics note: Duration is measured from start to stream establishment, not stream close
func (c *Client) DoStream(ctx context.Context, req Request) (io.ReadCloser, error) {
	scope, err := c.beginRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}

	resp, err := c.doHTTPRequest(scope.ctx, req)
	if err != nil {
		statusCode := extractStatusCode(err)
		// Caller-side build errors never reached the upstream — skip the
		// breaker entirely so neither RecordFailure nor RecordSuccess fires.
		if isLocalRequestBuildError(err) {
			c.finishRequest(scope, statusCode, err)
			return nil, err
		}
		c.completeScope(scope, statusCode, err, err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			respBody = []byte("failed to read error response")
		}
		_ = resp.Body.Close()

		providerErr := core.ParseProviderError(c.config.ProviderName, resp.StatusCode, respBody, nil)
		c.completeScope(scope, resp.StatusCode, providerErr, nil)
		return nil, providerErr
	}

	c.completeScope(scope, resp.StatusCode, nil, nil)
	return resp.Body, nil
}

func canRetryPassthrough(req Request) bool {
	if req.RawBodyReader != nil {
		return false
	}
	if hasIdempotencyKey(req.Headers) {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(req.Method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut:
		return true
	default:
		return false
	}
}

func hasIdempotencyKey(headers http.Header) bool {
	for key, values := range headers {
		if http.CanonicalHeaderKey(strings.TrimSpace(key)) != "Idempotency-Key" {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

// DoPassthrough executes a request and returns the raw upstream HTTP response.
// Unlike DoRaw, it preserves non-200 responses for the caller to proxy unchanged.
func (c *Client) DoPassthrough(ctx context.Context, req Request) (*http.Response, error) {
	stream := strings.Contains(strings.ToLower(strings.Join(req.Headers.Values("Accept"), ",")), "text/event-stream")
	scope, err := c.beginRequest(ctx, req, stream)
	if err != nil {
		return nil, err
	}
	ctx = scope.ctx

	maxAttempts := 1
	if canRetryPassthrough(req) {
		maxAttempts = c.maxAttempts()
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := c.waitForRetryAttempt(ctx, scope, attempt); err != nil {
			return nil, err
		}

		resp, err := c.doHTTPRequest(ctx, req)
		if err != nil {
			statusCode := extractStatusCode(err)
			// Caller-side build errors will repeat and never hit the upstream;
			// skip the breaker entirely (cbErr=nil would otherwise record a
			// spurious success for a 400-class status).
			if isLocalRequestBuildError(err) {
				c.finishRequest(scope, statusCode, err)
				return nil, err
			}
			if scope.halfOpenProbe || isClientTimeoutGatewayError(err) || attempt == maxAttempts-1 {
				c.completeScope(scope, statusCode, err, err)
				return nil, err
			}
			continue
		}

		retryable := c.isRetryable(resp.StatusCode)
		if retryable {
			if scope.halfOpenProbe || attempt == maxAttempts-1 {
				c.completeScope(scope, resp.StatusCode, nil, nil)
				return resp, nil
			}
			_ = resp.Body.Close()
			continue
		}

		c.completeScope(scope, resp.StatusCode, nil, nil)
		return resp, nil
	}

	return nil, c.failAfterRetries(scope)
}

// extractModel attempts to extract the model name from a request body
func extractModel(body any) string {
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

	// Try AudioSpeechRequest. Transcription has no JSON body (multipart upload),
	// so its model cannot be recovered here and stays "unknown".
	if speechReq, ok := body.(*core.AudioSpeechRequest); ok && speechReq != nil {
		return speechReq.Model
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

// doHTTPRequest executes a single HTTP request without retries and returns the
// live upstream response. Metrics hooks are called at the logical request
// level, not here, to avoid counting each attempt separately.
func (c *Client) doHTTPRequest(ctx context.Context, req Request) (*http.Response, error) {
	httpReq, err := c.buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, providerErrorStatusCode(err), "failed to send request: "+err.Error(), err)
	}
	return resp, nil
}

// doRequest executes a single HTTP request without retries.
// Note: Metrics hooks are called at the DoRaw level, not here, to avoid
// counting each retry attempt as a separate request.
func (c *Client) doRequest(ctx context.Context, req Request) (*Response, error) {
	resp, err := c.doHTTPRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, core.NewProviderError(c.config.ProviderName, providerErrorStatusCode(err), "failed to read response: "+err.Error(), err)
	}

	return &Response{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
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
	bodySources := 0
	if req.Body != nil {
		bodySources++
	}
	if req.RawBody != nil {
		bodySources++
	}
	if req.RawBodyReader != nil {
		bodySources++
	}
	if bodySources > 1 {
		return nil, core.NewInvalidRequestError("Body, RawBody, and RawBodyReader are mutually exclusive", nil)
	}
	if req.RawBodyReader != nil {
		bodyReader = req.RawBodyReader
	} else if req.RawBody != nil {
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
	for key, values := range req.Headers {
		httpReq.Header.Del(key)
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
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

func providerErrorStatusCode(err error) int {
	if isTimeoutError(err) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "client.timeout exceeded") ||
		strings.Contains(message, "timeout awaiting response headers")
}

// isLocalRequestBuildError reports whether err originated from buildRequest
// (e.g. an empty endpoint, an invalid HTTP method, mutually-exclusive bodies,
// or a marshal failure). Such errors are caller-side: they will repeat
// deterministically on retry and must not be charged to the circuit breaker
// because the upstream provider was never contacted.
//
// buildRequest is the only producer of *core.GatewayError with type
// ErrorTypeInvalidRequest along the doRequest/doHTTPRequest path — the other
// transport-layer wrappers all use NewProviderError. ParseProviderError runs
// only on a returned response body, so an InvalidRequest seen in the
// transport-error branch can only have come from buildRequest.
func isLocalRequestBuildError(err error) bool {
	if err == nil {
		return false
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return false
	}
	return gatewayErr.Type == core.ErrorTypeInvalidRequest
}

func isClientTimeoutGatewayError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return isTimeoutError(err)
	}
	if gatewayErr.StatusCode != http.StatusGatewayTimeout {
		return false
	}
	if isTimeoutError(gatewayErr.Err) {
		return true
	}
	return isTimeoutError(gatewayErr)
}
