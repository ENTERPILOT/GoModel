package server

import (
	"context"
	"sort"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// HeaderMutatorResolver resolves the request-scoped header-mutating workflow
// steps, in execution order. Implemented by the workflows service.
type HeaderMutatorResolver interface {
	HeaderMutatorsForContext(ctx context.Context) []core.HeaderMutator
}

// HeaderModificationMiddleware evaluates header_modification workflow steps
// against the inbound request headers. It must run after workflow resolution
// (steps are selected by the resolved workflow) and stores the merged
// outbound header mutation in the request context; the outbound builders for
// translated and passthrough routes apply it when constructing the provider
// request. Each step that changes anything is recorded on the audit entry's
// request-revision chain as an intended change, whether or not execution later
// reaches provider egress.
func HeaderModificationMiddleware(resolver HeaderMutatorResolver, auditLogger auditlog.LoggerInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			mutators := resolver.HeaderMutatorsForContext(c.Request().Context())
			if len(mutators) == 0 {
				return next(c)
			}

			inbound := c.Request().Header
			merged := &core.HeaderMutation{}
			for _, mutator := range mutators {
				mutation := mutator.HeaderMutation(inbound)
				if mutation.IsZero() {
					continue
				}
				recordHeaderRevision(c, auditLogger, mutator.Name(), mutation)
				merged.Merge(mutation)
			}

			if !merged.IsZero() {
				req := c.Request()
				c.SetRequest(req.WithContext(core.WithHeaderMutation(req.Context(), merged)))
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
func recordHeaderRevision(c *echo.Context, auditLogger auditlog.LoggerInterface, name string, mutation *core.HeaderMutation) {
	if auditLogger == nil {
		return
	}
	cfg := auditLogger.Config()
	if !cfg.Enabled {
		return
	}

	delta := &auditlog.HeaderRevisionSnapshot{}
	if len(mutation.Set) > 0 {
		if cfg.LogHeaders {
			delta.Set = auditlog.RedactHeaders(mutation.Set)
		} else {
			names := make([]string, 0, len(mutation.Set))
			for header := range mutation.Set {
				names = append(names, header)
			}
			sort.Strings(names)
			delta.Set = names
		}
	}
	if len(mutation.Remove) > 0 {
		delta.Removed = append([]string(nil), mutation.Remove...)
	}
	auditlog.EnrichEntryWithRequestRevision(c, auditlog.RequestRevisionSnapshot{
		Rewriter: name,
		Headers:  delta,
	})
}
