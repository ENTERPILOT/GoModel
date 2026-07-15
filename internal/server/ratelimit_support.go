package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

// RateLimiter admits or rejects requests against configured rate limit rules.
// RouteAvailable additionally satisfies gateway.RouteGate so failover can skip
// saturated provider/model targets.
type RateLimiter interface {
	Acquire(subjects ratelimit.Subjects, now time.Time) (*ratelimit.Reservation, error)
	RouteAvailable(providerName, model string) bool
}

func noopRelease() {}

// rateLimitRoute names the resolved provider/model a request is about to use,
// so provider- and model-scoped rules can be checked at admission. The zero
// value means the route is unknown (batch submissions) and only user-path
// rules apply.
type rateLimitRoute struct {
	provider string
	model    string
	// failovers counts the failover selectors configured for the request.
	// When positive, a provider/model-scoped breach defers to the failover
	// sweep instead of rejecting outright; consumer (user-path) breaches
	// always reject, since switching targets cannot relieve them.
	failovers int
}

// withFailovers records how many failover targets could serve the request.
func (r rateLimitRoute) withFailovers(count int) rateLimitRoute {
	r.failovers = count
	return r
}

// rateLimitRouteFromWorkflow extracts the resolved route for translated
// endpoints. Failover may still execute elsewhere; the failover sweep
// re-checks candidates through the route gate.
func rateLimitRouteFromWorkflow(workflow *core.Workflow) rateLimitRoute {
	if workflow == nil || workflow.Resolution == nil {
		return rateLimitRoute{}
	}
	return rateLimitRoute{
		provider: workflow.Resolution.ProviderName,
		model:    workflow.Resolution.ResolvedQualifiedModel(),
	}
}

// enforceRateLimit admits the request against matching rate limit rules. On
// success it sets x-ratelimit-* response headers and returns a release
// function that must run when the request finishes (it returns concurrency
// slots). On breach it returns a 429 gateway error.
func enforceRateLimit(c *echo.Context, limiter RateLimiter, route rateLimitRoute) (func(), error) {
	if limiter == nil || c == nil || c.Request() == nil {
		return noopRelease, nil
	}
	reservation, err := acquireRateLimitForContext(c.Request().Context(), limiter, route)
	if err != nil {
		return noopRelease, err
	}
	if reservation == nil {
		return noopRelease, nil
	}
	applyRateLimitHeaders(c.Response().Header(), reservation.Headers())
	return reservation.Release, nil
}

// admission is the outcome of the shared admission sequence. release must run
// when the request finishes. saturatedRoute, when set, is the 429 the client
// would have received for a provider/model-scoped breach: the request was
// admitted against its consumer limits anyway so dispatch can skip the
// saturated primary and sweep the configured failover targets.
type admission struct {
	release        func()
	saturatedRoute error
}

// dispatchContext stamps the saturated-route marker for the orchestrator.
func (a admission) dispatchContext(ctx context.Context) context.Context {
	return core.WithPrimaryRouteSaturated(ctx, a.saturatedRoute)
}

// enforceAdmission runs the shared admission sequence — rate limits first,
// then budget. On a budget rejection the reservation is released here, so
// callers never hold a concurrency slot for a refused request.
func enforceAdmission(c *echo.Context, limiter RateLimiter, checker BudgetChecker, route rateLimitRoute) (admission, error) {
	release, err := enforceRateLimit(c, limiter, route)
	var saturated error
	if err != nil {
		saturated = routeSaturationDeferrableToFailover(err, route)
		if saturated == nil {
			return admission{release: noopRelease}, err
		}
		// The saturated route defers to failover, but consumer limits still
		// gate (and count) the request, which may execute on another target.
		release, err = enforceRateLimit(c, limiter, rateLimitRoute{})
		if err != nil {
			return admission{release: noopRelease}, err
		}
	}
	if err := enforceBudget(c, checker); err != nil {
		release()
		return admission{release: noopRelease}, err
	}
	return admission{release: release, saturatedRoute: saturated}, nil
}

// routeSaturationDeferrableToFailover returns the rejection when it may defer
// to failover: only provider/model-scoped breaches qualify, and only when
// failover targets are configured for the request.
func routeSaturationDeferrableToFailover(err error, route rateLimitRoute) error {
	if route.failovers == 0 {
		return nil
	}
	var exceeded *ratelimit.ExceededError
	if !errors.As(err, &exceeded) {
		return nil
	}
	if exceeded.Rule.Scope != ratelimit.ScopeProvider && exceeded.Rule.Scope != ratelimit.ScopeModel {
		return nil
	}
	return err
}

func acquireRateLimitForContext(ctx context.Context, limiter RateLimiter, route rateLimitRoute) (*ratelimit.Reservation, error) {
	if limiter == nil || ctx == nil {
		return nil, nil
	}
	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}
	reservation, err := limiter.Acquire(ratelimit.Subjects{
		UserPath: userPath,
		Provider: route.provider,
		Model:    route.model,
	}, time.Now().UTC())
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
		// Keep the breach in the chain (Err is never serialized): admission
		// inspects the exceeded rule's scope to decide whether a saturated
		// route may defer to failover.
		gatewayErr.Err = exceeded
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
// pin a concurrency slot for its lifetime. The route is unknown at submission
// (batch files can mix models), so only user-path rules apply.
func batchRateLimitEnforcer(limiter RateLimiter) func(context.Context) error {
	return func(ctx context.Context) error {
		reservation, err := acquireRateLimitForContext(ctx, limiter, rateLimitRoute{})
		if err != nil {
			return err
		}
		if reservation != nil {
			reservation.Release()
		}
		return nil
	}
}
