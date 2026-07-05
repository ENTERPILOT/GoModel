package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/ratelimit"
)

// RateLimiter admits or rejects requests against configured rate limit rules.
type RateLimiter interface {
	Acquire(userPath string, now time.Time) (*ratelimit.Reservation, error)
}

func noopRelease() {}

// enforceRateLimit admits the request against matching rate limit rules. On
// success it sets x-ratelimit-* response headers and returns a release
// function that must run when the request finishes (it returns concurrency
// slots). On breach it returns a 429 gateway error.
func enforceRateLimit(c *echo.Context, limiter RateLimiter) (func(), error) {
	if limiter == nil || c == nil || c.Request() == nil {
		return noopRelease, nil
	}
	reservation, err := acquireRateLimitForContext(c.Request().Context(), limiter)
	if err != nil {
		return noopRelease, err
	}
	if reservation == nil {
		return noopRelease, nil
	}
	applyRateLimitHeaders(c.Response().Header(), reservation.Headers())
	return reservation.Release, nil
}

func acquireRateLimitForContext(ctx context.Context, limiter RateLimiter) (*ratelimit.Reservation, error) {
	if limiter == nil || ctx == nil {
		return nil, nil
	}
	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}
	reservation, err := limiter.Acquire(userPath, time.Now().UTC())
	if err != nil {
		return nil, rateLimitCheckError(err)
	}
	return reservation, nil
}

func rateLimitCheckError(err error) error {
	var exceeded *ratelimit.ExceededError
	if errors.As(err, &exceeded) {
		message := exceeded.Error()
		if message == "" {
			message = "rate limit exceeded"
		}
		gatewayErr := core.NewRateLimitError("ratelimit", message).WithCode("rate_limit_exceeded")
		return &gatewayErrorWithResponseHeaders{
			GatewayError: gatewayErr,
			headers:      rateLimitBreachHeaders(exceeded),
		}
	}
	return core.NewProviderError("ratelimit", http.StatusServiceUnavailable, "rate limit check failed", err).
		WithCode("rate_limit_check_failed")
}

func rateLimitBreachHeaders(exceeded *ratelimit.ExceededError) http.Header {
	headers := http.Header{}
	headers.Set("Retry-After", strconv.FormatInt(retryAfterSeconds(exceeded.RetryAfter), 10))
	reset := strconv.FormatInt(retryAfterSeconds(exceeded.RetryAfter), 10)
	limit := strconv.FormatInt(exceeded.Limit, 10)
	switch exceeded.Scope {
	case ratelimit.ScopeRequests:
		headers.Set("x-ratelimit-limit-requests", limit)
		headers.Set("x-ratelimit-remaining-requests", "0")
		headers.Set("x-ratelimit-reset-requests", reset)
	case ratelimit.ScopeTokens:
		headers.Set("x-ratelimit-limit-tokens", limit)
		headers.Set("x-ratelimit-remaining-tokens", "0")
		headers.Set("x-ratelimit-reset-tokens", reset)
	}
	return headers
}

func applyRateLimitHeaders(target http.Header, snapshot ratelimit.HeaderSnapshot) {
	if snapshot.HasRequests {
		target.Set("x-ratelimit-limit-requests", strconv.FormatInt(snapshot.RequestLimit, 10))
		target.Set("x-ratelimit-remaining-requests", strconv.FormatInt(snapshot.RequestRemaining, 10))
		target.Set("x-ratelimit-reset-requests", strconv.FormatInt(retryAfterSeconds(snapshot.RequestResetAfter), 10))
	}
	if snapshot.HasTokens {
		target.Set("x-ratelimit-limit-tokens", strconv.FormatInt(snapshot.TokenLimit, 10))
		target.Set("x-ratelimit-remaining-tokens", strconv.FormatInt(snapshot.TokenRemaining, 10))
		target.Set("x-ratelimit-reset-tokens", strconv.FormatInt(retryAfterSeconds(snapshot.TokenResetAfter), 10))
	}
}

func retryAfterSeconds(d time.Duration) int64 {
	seconds := int64(math.Ceil(d.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

// batchRateLimitEnforcer counts a batch submission toward request windows.
// The reservation is released immediately: an asynchronous batch job must not
// pin a concurrency slot for its lifetime.
func batchRateLimitEnforcer(limiter RateLimiter) func(context.Context) error {
	return func(ctx context.Context) error {
		reservation, err := acquireRateLimitForContext(ctx, limiter)
		if err != nil {
			return err
		}
		if reservation != nil {
			reservation.Release()
		}
		return nil
	}
}
