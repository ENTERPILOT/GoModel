package server

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// admitInternal runs the admission sequence for an internal call that no
// HTTP admission covered, such as a request rewriter's call made before its
// request is admitted: rate limits first, then budget, against the call's
// user path and resolved route. A provider- or model-scoped breach rejects
// instead of deferring to failover. release must run when the call finishes.
func admitInternal(ctx context.Context, limiter RateLimiter, checker BudgetChecker, workflow *core.Workflow) (release func(), err error) {
	route := rateLimitRouteFromWorkflow(workflow)
	release = noopRelease
	if limiter != nil {
		reservation, err := acquireRateLimitForContext(ctx, limiter, route)
		if err != nil {
			return noopRelease, err
		}
		if reservation != nil {
			release = reservation.Release
		}
	}
	if checker != nil && (workflow == nil || workflow.BudgetEnabled()) {
		if err := checker.Check(ctx, budgetSubjectsFromContext(ctx, route), time.Now().UTC()); err != nil {
			release()
			return noopRelease, budgetCheckError(err)
		}
	}
	return release, nil
}
