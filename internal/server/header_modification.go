package server

import (
	"context"
	"sort"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// HeaderPolicyResolver resolves request-scoped outbound policies in execution
// order. Implemented by the workflows service.
type HeaderPolicyResolver interface {
	HeaderPoliciesForContext(ctx context.Context) []core.HeaderPolicy
}

// HeaderPolicyPlanningMiddleware evaluates outbound header policies after
// workflow resolution and before any cache lookup. It stores one immutable,
// fully resolved plan for the primary provider route. Each matching policy is
// recorded as intended change; the revision is not proof of provider egress.
func HeaderPolicyPlanningMiddleware(resolver HeaderPolicyResolver, auditLogger auditlog.LoggerInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			policies := resolver.HeaderPoliciesForContext(c.Request().Context())
			if len(policies) == 0 {
				return next(c)
			}

			inbound := c.Request().Header
			input := core.HeaderPolicyInput{
				Headers: inbound,
				Method:  c.Request().Method,
				Path:    c.Request().URL.Path,
			}
			merged := &core.HeaderPlan{}
			for _, policy := range policies {
				plan := policy.ResolveHeaderPlan(input)
				if plan.IsZero() {
					continue
				}
				recordHeaderRevision(c, auditLogger, policy.Name(), plan)
				merged.Merge(plan)
			}

			if !merged.IsZero() {
				req := c.Request()
				c.SetRequest(req.WithContext(core.WithHeaderPlan(req.Context(), merged)))
			}
			return next(c)
		}
	}
}

// recordHeaderRevision appends one header-modification entry to the audit
// trail's request-revision chain. Only the delta is stored. Set values follow
// the same LOGGING_LOG_HEADERS gate and redaction policy as normal request
// headers; when value logging is disabled only the set header names are kept.
// Body fields stay empty because header steps never touch the body.
func recordHeaderRevision(c *echo.Context, auditLogger auditlog.LoggerInterface, name string, plan *core.HeaderPlan) {
	if auditLogger == nil {
		return
	}
	cfg := auditLogger.Config()
	if !cfg.Enabled {
		return
	}

	delta := &auditlog.HeaderRevisionSnapshot{}
	if len(plan.Set) > 0 {
		if cfg.LogHeaders {
			values := auditlog.RedactHeaders(plan.Set)
			for _, header := range plan.SensitiveSet {
				if _, exists := values[header]; exists {
					values[header] = auditlog.RedactedHeaderValue
				}
			}
			delta.Set = values
		} else {
			names := make([]string, 0, len(plan.Set))
			for header := range plan.Set {
				names = append(names, header)
			}
			sort.Strings(names)
			delta.Set = names
		}
	}
	if len(plan.Remove) > 0 {
		delta.Removed = append([]string(nil), plan.Remove...)
	}
	auditlog.EnrichEntryWithRequestRevision(c, auditlog.RequestRevisionSnapshot{
		Rewriter: name,
		Headers:  delta,
	})
}
