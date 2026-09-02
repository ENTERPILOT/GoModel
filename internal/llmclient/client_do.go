package llmclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// attachResponseHeaders records the upstream response headers on a provider
// GatewayError so failed attempts can be audited. It is a no-op for other
// error types or a nil header set.
func attachResponseHeaders(err error, header http.Header) error {
	if header == nil {
		return err
	}
	var gatewayErr *core.GatewayError
	if errors.As(err, &gatewayErr) && gatewayErr != nil {
		gatewayErr.ResponseHeaders = header.Clone()
	}
	return err
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
		closeRawBodyReader(req)
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
			closeRawBodyReader(req)
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
				c.finishRequestWithoutBreaker(scope, lastStatusCode, err)
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
			lastErr = attachResponseHeaders(core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil), resp.Header)
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
			parsedErr := attachResponseHeaders(core.ParseProviderError(c.config.ProviderName, resp.StatusCode, resp.Body, nil), resp.Header)
			c.completeScope(scope, resp.StatusCode, parsedErr, nil)
			return nil, parsedErr
		}

		// Some providers answer 200 with a bare {"error": ...} body; map it
		// here so the status drives retries, the breaker, and metrics.
		if embedded := core.ParseEmbeddedProviderError(c.config.ProviderName, resp.Body); embedded != nil {
			lastErr = attachResponseHeaders(embedded, resp.Header)
			lastStatusCode = embedded.StatusCode
			lastErrFromTransport = false
			if c.isRetryable(embedded.StatusCode) && !scope.halfOpenProbe {
				continue
			}
			c.completeScope(scope, lastStatusCode, lastErr, nil)
			return nil, lastErr
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
		closeRawBodyReader(req)
		return nil, err
	}

	resp, err := c.doHTTPRequest(scope.ctx, req)
	if err != nil {
		statusCode := extractStatusCode(err)
		// Caller-side build errors never reached the upstream — skip the
		// breaker entirely so neither RecordFailure nor RecordSuccess fires.
		if isLocalRequestBuildError(err) {
			c.finishRequestWithoutBreaker(scope, statusCode, err)
			return nil, err
		}
		c.completeScope(scope, statusCode, err, err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		if readErr != nil {
			respBody = []byte("failed to read error response")
		}
		_ = resp.Body.Close()

		providerErr := attachResponseHeaders(core.ParseProviderError(c.config.ProviderName, resp.StatusCode, respBody, nil), resp.Header)
		c.completeScope(scope, resp.StatusCode, providerErr, nil)
		return nil, providerErr
	}

	// A 200 stream answered with a buffered {"error": ...} object failed
	// despite its status. Detect it before the scope completes so the breaker
	// and metrics record the failure; later layers cannot revise the outcome.
	// Errors arriving mid-stream inside genuine SSE remain the caller's.
	if embedded := interceptEmbeddedStreamError(c.config.ProviderName, resp); embedded != nil {
		providerErr := attachResponseHeaders(embedded, resp.Header)
		c.completeScope(scope, embedded.StatusCode, providerErr, nil)
		return nil, providerErr
	}

	// The stream can outlive the request by minutes while transport internals
	// keep resp.Request reachable. GetBody closes over the fully marshaled
	// request payload; redirects and transparent transport retries only
	// consult it inside Do, so dropping it here releases the payload for the
	// stream's lifetime without changing behavior.
	if resp.Request != nil {
		resp.Request.GetBody = nil
	}

	c.completeScope(scope, resp.StatusCode, nil, nil)
	c.observeFirstChunk(scope, resp, true)
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
	stream := req.Stream || strings.Contains(strings.ToLower(strings.Join(req.Headers.Values("Accept"), ",")), "text/event-stream")
	scope, err := c.beginRequest(ctx, req, stream)
	if err != nil {
		closeRawBodyReader(req)
		return nil, err
	}
	ctx = scope.ctx

	maxAttempts := 1
	if canRetryPassthrough(req) {
		maxAttempts = c.maxAttempts()
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := c.waitForRetryAttempt(ctx, scope, attempt); err != nil {
			closeRawBodyReader(req)
			return nil, err
		}

		resp, err := c.doHTTPRequest(ctx, req)
		if err != nil {
			statusCode := extractStatusCode(err)
			// Caller-side build errors will repeat and never hit the upstream;
			// skip the breaker entirely (cbErr=nil would otherwise record a
			// spurious success for a 400-class status).
			if isLocalRequestBuildError(err) {
				c.finishRequestWithoutBreaker(scope, statusCode, err)
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

		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			// The response settles what a bounded body peek could not: an
			// uncertain request that came back as JSON completes here as a
			// buffered call, so hooks see the resolved stream state.
			responseStream := stream || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
			scope.requestInfo.Stream = responseStream
			scope.requestInfo.StreamUncertain = false
			c.completeScope(scope, resp.StatusCode, nil, nil)
			c.observeFirstChunk(scope, resp, responseStream)
			return resp, nil
		}
		c.completeScope(scope, resp.StatusCode, nil, nil)
		return resp, nil
	}

	return nil, c.failAfterRetries(scope)
}
