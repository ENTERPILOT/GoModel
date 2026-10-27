package server

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// admitInternal runs the HTTP admission sequence for an internal call that no
// HTTP admission covered, such as a request rewriter's call made before its
// request is admitted: rate limits, then budget, against the call's user path
// and resolved route. As over HTTP, an exhausted provider or model limit
// defers to the workflow's failover targets; the returned context carries
// that to dispatch. release must run when the call finishes.
func admitInternal(ctx context.Context, limiter RateLimiter, checker BudgetChecker, workflow *core.Workflow, failovers int) (context.Context, func(), error) {
	route := rateLimitRouteFromWorkflow(workflow).withFailovers(failovers)
	adm, err := admit(core.WithWorkflow(ctx, workflow), limiter, checker, route, nil)
	if err != nil {
		return ctx, noopRelease, err
	}
	return adm.dispatchContext(ctx), adm.release, nil
}
