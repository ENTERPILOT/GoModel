package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
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
	return reserveRateLimit(c.Request().Context(), limiter, route, rateLimitHeaderSetter(c))
}

// rateLimitHeaderSetter copies an admitted reservation's x-ratelimit-*
// headers onto the response.
func rateLimitHeaderSetter(c *echo.Context) func(*ratelimit.Reservation) {
	return func(reservation *ratelimit.Reservation) {
		applyRateLimitHeaders(c.Response().Header(), reservation.Headers())
	}
}

// reserveRateLimit admits ctx against matching rate limit rules and passes an
// admitted reservation to onReserve, when set. It returns the reservation's
// release function.
func reserveRateLimit(ctx context.Context, limiter RateLimiter, route rateLimitRoute, onReserve func(*ratelimit.Reservation)) (func(), error) {
	if limiter == nil {
		return noopRelease, nil
	}
	reservation, err := acquireRateLimitForContext(ctx, limiter, route)
	if err != nil {
		return noopRelease, err
	}
	if reservation == nil {
		return noopRelease, nil
	}
	if onReserve != nil {
		onReserve(reservation)
	}
	return reservation.Release, nil
}

// admission is the outcome of the shared admission sequence. release must run
// when the request finishes. saturatedRoute, when set, is the 429 the client
// would have received for a provider/model-scoped breach. Dispatch skips the
// saturated primary; consumer quota is committed when an alternate is admitted.
type admission struct {
	release        func()
	saturatedRoute error
	attempts       *requestAttemptAdmission
}

// dispatchContext stamps the saturated-route marker for the orchestrator.
func (a admission) dispatchContext(ctx context.Context) context.Context {
	if a.attempts != nil {
		ctx = gateway.WithAttemptAdmission(ctx, a.attempts)
	} else {
		ctx = gateway.WithAttemptAdmission(ctx, nil)
	}
	return core.WithPrimaryRouteSaturated(ctx, a.saturatedRoute)
}

func (a admission) applyHeaders(c *echo.Context) {
	if a.attempts != nil {
		snapshot := a.attempts.headers
		elapsed := time.Since(a.attempts.headersAt)
		snapshot.RequestResetAfter = max(snapshot.RequestResetAfter-elapsed, 0)
		snapshot.TokenResetAfter = max(snapshot.TokenResetAfter-elapsed, 0)
		headers := c.Response().Header()
		for _, name := range []string{rateLimitLimitRequestsHeader, rateLimitRemainingRequestsHeader, rateLimitResetRequestsHeader, rateLimitLimitTokensHeader, rateLimitRemainingTokensHeader, rateLimitResetTokensHeader} {
			headers.Del(name)
		}
		applyRateLimitHeaders(headers, snapshot)
	}
}

// enforceAdmission checks budgets and rate limits. Resolved routes check budget
// before committing request counters, so rejected requests consume no quota.
func enforceAdmission(c *echo.Context, limiter RateLimiter, checker BudgetChecker, route rateLimitRoute) (admission, error) {
	if c == nil || c.Request() == nil {
		return admission{release: noopRelease}, nil
	}
	return admit(c.Request().Context(), limiter, checker, route, rateLimitHeaderSetter(c))
}

// admit is enforceAdmission over a context, for callers without an HTTP
// exchange. onReserve, when set, receives each admitted reservation.
func admit(ctx context.Context, limiter RateLimiter, checker BudgetChecker, route rateLimitRoute, onReserve func(*ratelimit.Reservation)) (admission, error) {
	if limiter != nil && route.provider != "" && route.model != "" {
		return admitAttemptRequest(ctx, limiter, checker, route, onReserve)
	}
	release, err := reserveRateLimit(ctx, limiter, route, onReserve)
	var saturated error
	if err != nil {
		saturated = routeSaturationDeferrableToFailover(err, route)
		if saturated == nil {
			return admission{release: noopRelease}, err
		}
		// The saturated route defers to failover, but consumer limits still
		// gate (and count) the request, which may execute on another target.
		release, err = reserveRateLimit(ctx, limiter, rateLimitRoute{}, onReserve)
		if err != nil {
			return admission{release: noopRelease}, err
		}
	}
	if err := enforceBudgetForContext(ctx, checker, route); err != nil {
		deferred := providerBudgetDeferrableToFailover(err, route)
		if deferred == nil {
			release()
			return admission{release: noopRelease}, err
		}
		// Consumer budgets sort ahead of provider budgets, so reaching an
		// exhausted provider budget means every consumer budget passed.
		if saturated == nil {
			saturated = deferred
		}
	}
	return admission{release: release, saturatedRoute: saturated}, nil
}

// providerBudgetDeferrableToFailover returns the rejection when an exhausted
// provider budget may defer to failover, which skips targets whose provider
// budget is also spent.
func providerBudgetDeferrableToFailover(err error, route rateLimitRoute) error {
	if route.failovers == 0 {
		return nil
	}
	exceeded, ok := errors.AsType[*budget.ExceededError](err)
	if !ok || exceeded.Result.Budget.Scope != budget.ScopeProvider {
		return nil
	}
	return err
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
	if exceeded, ok := errors.AsType[*ratelimit.ExceededError](err); ok {
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

// Rate-limit response headers in canonical textproto form: Header.Set only
// skips copying/canonicalizing the key when it is already canonical, and these
// are set on every rate-limited request.
const (
	rateLimitLimitRequestsHeader     = "X-Ratelimit-Limit-Requests"
	rateLimitRemainingRequestsHeader = "X-Ratelimit-Remaining-Requests"
	rateLimitResetRequestsHeader     = "X-Ratelimit-Reset-Requests"
	rateLimitLimitTokensHeader       = "X-Ratelimit-Limit-Tokens"     //nolint:gosec // G101: header name, not a credential.
	rateLimitRemainingTokensHeader   = "X-Ratelimit-Remaining-Tokens" //nolint:gosec // G101: header name, not a credential.
	rateLimitResetTokensHeader       = "X-Ratelimit-Reset-Tokens"     //nolint:gosec // G101: header name, not a credential.
)

func rateLimitBreachHeaders(exceeded *ratelimit.ExceededError) http.Header {
	headers := http.Header{}
	headers.Set("Retry-After", strconv.FormatInt(retryAfterSeconds(exceeded.RetryAfter), 10))
	reset := strconv.FormatInt(retryAfterSeconds(exceeded.RetryAfter), 10)
	limit := strconv.FormatInt(exceeded.Limit, 10)
	switch exceeded.Scope {
	case ratelimit.ScopeRequests:
		headers.Set(rateLimitLimitRequestsHeader, limit)
		headers.Set(rateLimitRemainingRequestsHeader, "0")
		headers.Set(rateLimitResetRequestsHeader, reset)
	case ratelimit.ScopeTokens:
		headers.Set(rateLimitLimitTokensHeader, limit)
		headers.Set(rateLimitRemainingTokensHeader, "0")
		headers.Set(rateLimitResetTokensHeader, reset)
	case ratelimit.ScopeConcurrency:
		// An in-flight gauge has no window: the cap and the zero headroom are
		// well defined, a reset time is not — a slot frees when a request
		// finishes. Retry-After carries the retry hint instead, so
		// x-ratelimit-reset-requests is deliberately omitted here.
		headers.Set(rateLimitLimitRequestsHeader, limit)
		headers.Set(rateLimitRemainingRequestsHeader, "0")
	}
	return headers
}

func applyRateLimitHeaders(target http.Header, snapshot ratelimit.HeaderSnapshot) {
	if snapshot.HasRequests {
		target.Set(rateLimitLimitRequestsHeader, strconv.FormatInt(snapshot.RequestLimit, 10))
		target.Set(rateLimitRemainingRequestsHeader, strconv.FormatInt(snapshot.RequestRemaining, 10))
		target.Set(rateLimitResetRequestsHeader, strconv.FormatInt(max(int64(math.Ceil(snapshot.RequestResetAfter.Seconds())), 0), 10))
	}
	if snapshot.HasTokens {
		target.Set(rateLimitLimitTokensHeader, strconv.FormatInt(snapshot.TokenLimit, 10))
		target.Set(rateLimitRemainingTokensHeader, strconv.FormatInt(snapshot.TokenRemaining, 10))
		target.Set(rateLimitResetTokensHeader, strconv.FormatInt(max(int64(math.Ceil(snapshot.TokenResetAfter.Seconds())), 0), 10))
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
