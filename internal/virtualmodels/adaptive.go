package virtualmodels

import (
	"log/slog"

	"github.com/enterpilot/gomodel/ext"
)

// adaptiveTarget delegates the choice among the viable pool to the installed
// route selector. It reports false — sending the caller to weighted round
// robin — when no selector is installed, the selector declines, it answers
// with a model outside the pool, or it panics. Selectors are extension code
// running on the request path, so a panic is contained here rather than
// failing the request.
func (s *Service) adaptiveTarget(entry redirectEntry, sessionID string, pool []resolvedTarget) (target resolvedTarget, ok bool) {
	selector := s.routeSelector
	if selector == nil {
		return resolvedTarget{}, false
	}
	defer func() {
		// The recovered value is extension-controlled and may carry request
		// data, and calling back into the selector (even Name) mid-panic
		// could panic again — log only fixed metadata captured at install.
		if recover() != nil {
			slog.Error("route selector panicked; falling back to round robin",
				"selector", s.routeSelectorName, "source", entry.vm.Source)
			target, ok = resolvedTarget{}, false
		}
	}()

	req := ext.RouteRequest{
		Source:     entry.vm.Source,
		SessionID:  sessionID,
		Candidates: make([]ext.RouteCandidate, len(pool)),
	}
	for i, t := range pool {
		candidate := ext.RouteCandidate{
			Provider:  t.selector.Provider,
			Model:     t.selector.Model,
			Qualified: t.qualified,
			Weight:    t.weight,
		}
		if model, found := s.catalog.LookupModel(t.qualified); found && model != nil && model.Metadata != nil && model.Metadata.Pricing != nil {
			// Copies, not the catalog's pointers: extension code must not be
			// able to mutate shared pricing (or race catalog updates).
			candidate.InputPerMtok = copyPrice(model.Metadata.Pricing.InputPerMtok)
			candidate.OutputPerMtok = copyPrice(model.Metadata.Pricing.OutputPerMtok)
		}
		req.Candidates[i] = candidate
	}

	qualified, answered := selector.Select(req)
	if !answered {
		return resolvedTarget{}, false
	}
	return poolTarget(pool, qualified)
}

// copyPrice clones an optional per-Mtok price.
func copyPrice(price *float64) *float64 {
	if price == nil {
		return nil
	}
	v := *price
	return &v
}
