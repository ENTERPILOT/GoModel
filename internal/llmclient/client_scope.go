package llmclient

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

type requestScope struct {
	ctx           context.Context
	startedAt     time.Time
	requestInfo   RequestInfo
	breaker       *circuitBreaker
	halfOpenProbe bool
}

func (c *Client) beginRequest(ctx context.Context, req Request, stream bool) (requestScope, error) {
	scope := requestScope{
		ctx:       ctx,
		startedAt: time.Now(),
		requestInfo: RequestInfo{
			Provider:        c.config.ProviderName,
			Model:           requestModel(req),
			Operation:       req.Operation,
			Endpoint:        req.Endpoint,
			Method:          req.Method,
			Stream:          stream,
			StreamUncertain: req.StreamUncertain,
		},
	}

	if c.config.Hooks.OnRequestStart != nil {
		scope.ctx = c.config.Hooks.OnRequestStart(scope.ctx, scope.requestInfo)
	}

	scope.breaker = c.breakerForModel(scope.requestInfo.Model)
	if scope.breaker != nil {
		allowed, probe := scope.breaker.acquire()
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

func requestModel(req Request) string {
	if model := strings.TrimSpace(req.Model); model != "" {
		return model
	}
	return extractModel(req.Body)
}

func (c *Client) finishRequest(scope requestScope, statusCode int, err error) {
	if c.config.Hooks.OnRequestEnd == nil {
		return
	}
	circuitState := ""
	if scope.breaker != nil {
		circuitState = scope.breaker.State()
	}
	c.config.Hooks.OnRequestEnd(scope.ctx, ResponseInfo{
		Provider:        c.config.ProviderName,
		ProviderType:    scope.requestInfo.ProviderType,
		Model:           scope.requestInfo.Model,
		Operation:       scope.requestInfo.Operation,
		Endpoint:        scope.requestInfo.Endpoint,
		Method:          scope.requestInfo.Method,
		StatusCode:      statusCode,
		Duration:        time.Since(scope.startedAt),
		Stream:          scope.requestInfo.Stream,
		StreamUncertain: scope.requestInfo.StreamUncertain,
		Error:           err,
		CircuitState:    circuitState,
	})
}

func (c *Client) finishStreamFirstChunk(scope requestScope, statusCode int) {
	if c.config.Hooks.OnStreamFirstChunk == nil {
		return
	}
	c.config.Hooks.OnStreamFirstChunk(scope.ctx, ResponseInfo{
		Provider:        c.config.ProviderName,
		ProviderType:    scope.requestInfo.ProviderType,
		Model:           scope.requestInfo.Model,
		Operation:       scope.requestInfo.Operation,
		Endpoint:        scope.requestInfo.Endpoint,
		Method:          scope.requestInfo.Method,
		StatusCode:      statusCode,
		Duration:        time.Since(scope.startedAt),
		Stream:          true,
		StreamUncertain: scope.requestInfo.StreamUncertain,
	})
}

func (c *Client) finishStreamEmpty(scope requestScope, statusCode int, err error) {
	if c.config.Hooks.OnStreamEmpty == nil {
		return
	}
	c.config.Hooks.OnStreamEmpty(scope.ctx, ResponseInfo{
		Provider:     c.config.ProviderName,
		ProviderType: scope.requestInfo.ProviderType,
		Model:        scope.requestInfo.Model,
		Operation:    scope.requestInfo.Operation,
		Endpoint:     scope.requestInfo.Endpoint,
		Method:       scope.requestInfo.Method,
		StatusCode:   statusCode,
		Duration:     time.Since(scope.startedAt),
		Stream:       true,
		Error:        err,
	})
}

func (c *Client) observeFirstChunk(scope requestScope, resp *http.Response, stream bool) {
	if resp == nil || resp.Body == nil || !stream {
		return
	}
	resp.Body = &firstChunkReadCloser{
		ReadCloser: resp.Body,
		onFirstChunk: func() {
			c.finishStreamFirstChunk(scope, resp.StatusCode)
		},
		onEmpty: func(err error) {
			c.finishStreamEmpty(scope, resp.StatusCode, err)
		},
	}
}

// firstChunkReadCloser reports the first moment a stream body delivers bytes,
// or that it ended before delivering any. Exactly one of the two fires.
type firstChunkReadCloser struct {
	io.ReadCloser
	once         sync.Once
	onFirstChunk func()
	onEmpty      func(err error)
}

func (r *firstChunkReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	switch {
	case n > 0:
		r.once.Do(r.onFirstChunk)
	case err != nil:
		r.once.Do(func() { r.onEmpty(err) })
	}
	return n, err
}

// completeScope is the standard terminal step for a request that has passed
// beginRequest. It records the circuit-breaker outcome (using cbErr to decide
// whether the failure was transport-level) and emits the metrics observation.
// Use this whenever a code path returns from one of the public Do* methods.
func (c *Client) completeScope(scope requestScope, statusCode int, err, cbErr error) {
	c.recordCircuitBreakerCompletion(scope, statusCode, cbErr)
	c.finishRequest(scope, statusCode, err)
}

// finishRequestWithoutBreaker finalises a request that never reached the
// upstream (local request-build errors): no breaker outcome is recorded, but
// a consumed half-open probe slot must still be returned or the breaker would
// reject all traffic forever.
func (c *Client) finishRequestWithoutBreaker(scope requestScope, statusCode int, err error) {
	c.releaseHalfOpenProbe(scope)
	c.finishRequest(scope, statusCode, err)
}

// releaseHalfOpenProbe frees the breaker's probe slot when this request held
// it but ended without a success/failure verdict.
func (c *Client) releaseHalfOpenProbe(scope requestScope) {
	if scope.breaker != nil && scope.halfOpenProbe {
		scope.breaker.releaseProbe()
	}
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

func (c *Client) recordCircuitBreakerCompletion(scope requestScope, statusCode int, err error) {
	if scope.breaker == nil {
		return
	}
	if err != nil {
		// A caller-side cancellation aborts the transport but proves nothing
		// about provider health, so it is neither a success nor a failure.
		// Client deadlines (context.DeadlineExceeded) still count: the
		// provider failed to answer within the latency budget.
		if errors.Is(err, context.Canceled) {
			c.releaseHalfOpenProbe(scope)
			return
		}
		scope.breaker.RecordFailure()
		return
	}
	if c.shouldTripCircuitBreaker(statusCode) {
		scope.breaker.RecordFailure()
		return
	}
	scope.breaker.RecordSuccess()
}

func (c *Client) shouldTripCircuitBreaker(statusCode int) bool {
	return c.failureStatuses[statusCode]
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
	// A stopped timer releases its resources immediately; time.After would
	// keep the timer alive until it fires even after the context is cancelled.
	timer := time.NewTimer(c.calculateBackoff(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	return c.retryStatuses[statusCode]
}
