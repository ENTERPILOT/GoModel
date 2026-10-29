package server

import (
	"context"
	"errors"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/ratelimit"
)

type requestAttemptAdmission struct {
	limiter    RateLimiter
	consumer   *ratelimit.Reservation
	pending    *ratelimit.Reservation
	headers    ratelimit.HeaderSnapshot
	consumerAt time.Time
	headersAt  time.Time
}

type terminalAdmissionError struct{ error }

func (e terminalAdmissionError) Unwrap() error      { return e.error }
func (e terminalAdmissionError) StopFailover() bool { return true }

type routeAdmissionError struct{ error }

func (e routeAdmissionError) Unwrap() error       { return e.error }
func (e routeAdmissionError) RouteRejected() bool { return true }

func (a *requestAttemptAdmission) Acquire(ctx context.Context, provider, model string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return noopRelease, err
	}
	if a.pending != nil {
		reservation := a.pending
		a.pending = nil
		return reservation.ReleaseRoute, nil
	}
	subjects := ratelimit.Subjects{Provider: provider, Model: model}
	if a.consumer == nil {
		subjects.UserPath = core.UserPathFromContext(ctx)
		if subjects.UserPath == "" {
			subjects.UserPath = "/"
		}
	}
	reservation, err := a.limiter.Acquire(subjects, time.Now().UTC())
	if err != nil {
		var exceeded *ratelimit.ExceededError
		if errors.As(err, &exceeded) && exceeded.Rule.Scope == ratelimit.ScopeUserPath {
			return noopRelease, terminalAdmissionError{rateLimitCheckError(err)}
		}
		if exceeded != nil && (exceeded.Rule.Scope == ratelimit.ScopeProvider || exceeded.Rule.Scope == ratelimit.ScopeModel) {
			return noopRelease, routeAdmissionError{rateLimitCheckError(err)}
		}
		return noopRelease, terminalAdmissionError{rateLimitCheckError(err)}
	}
	if a.consumer == nil {
		a.consumer = reservation
		a.consumerAt = time.Now()
		a.headers = reservation.Headers()
		a.headersAt = a.consumerAt
		return reservation.ReleaseRoute, nil
	}
	consumerHeaders := a.consumer.ConsumerHeaders()
	elapsed := time.Since(a.consumerAt)
	consumerHeaders.RequestResetAfter = max(consumerHeaders.RequestResetAfter-elapsed, 0)
	consumerHeaders.TokenResetAfter = max(consumerHeaders.TokenResetAfter-elapsed, 0)
	a.headers = mergeAttemptHeaders(consumerHeaders, reservation.Headers())
	a.headersAt = time.Now()
	return reservation.Release, nil
}

func mergeAttemptHeaders(consumer, route ratelimit.HeaderSnapshot) ratelimit.HeaderSnapshot {
	if consumer.HasRequests && (!route.HasRequests || consumer.RequestRemaining <= route.RequestRemaining) {
		route.HasRequests = true
		route.RequestLimit, route.RequestRemaining, route.RequestResetAfter = consumer.RequestLimit, consumer.RequestRemaining, consumer.RequestResetAfter
	}
	if consumer.HasTokens && (!route.HasTokens || consumer.TokenRemaining <= route.TokenRemaining) {
		route.HasTokens = true
		route.TokenLimit, route.TokenRemaining, route.TokenResetAfter = consumer.TokenLimit, consumer.TokenRemaining, consumer.TokenResetAfter
	}
	return route
}

func (a *requestAttemptAdmission) release() {
	a.consumer.Release()
}

func admitAttemptRequest(ctx context.Context, limiter RateLimiter, checker BudgetChecker, route rateLimitRoute, onReserve func(*ratelimit.Reservation)) (admission, error) {
	var saturated error
	if err := enforceBudgetForContext(ctx, checker, route); err != nil {
		saturated = providerBudgetDeferrableToFailover(err, route)
		if saturated == nil {
			return admission{release: noopRelease}, err
		}
	}
	var reservation *ratelimit.Reservation
	if saturated == nil {
		var err error
		reservation, err = acquireRateLimitForContext(ctx, limiter, route)
		if err != nil {
			saturated = routeSaturationDeferrableToFailover(err, route)
			if saturated == nil {
				return admission{release: noopRelease}, err
			}
		}
	}
	now := time.Now()
	state := &requestAttemptAdmission{limiter: limiter, consumer: reservation, pending: reservation, headers: reservation.Headers(), consumerAt: now, headersAt: now}
	if reservation != nil && onReserve != nil {
		onReserve(reservation)
	}
	return admission{release: state.release, saturatedRoute: saturated, attempts: state}, nil
}
