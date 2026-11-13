package virtualmodels

import (
	"context"

	"github.com/enterpilot/gomodel/internal/requestparams"
)

// SubjectRequestPolicy is an optional AccessPolicy capability: the request
// policy the subject side (the user path) sets for a request, whatever model
// it goes to.
type SubjectRequestPolicy interface {
	// RequestCeilings returns the ceilings that bound every attempt of the
	// request.
	RequestCeilings(ctx context.Context) requestparams.Ceilings
	// DefaultModel returns the model that serves the request when it names
	// none, or "".
	DefaultModel(ctx context.Context) string
}

// DefaultModel returns the model the subject-side policy assigns to a
// request that names none, or "" when it assigns none. The caller resolves
// it like a model the client sent, so it may name a redirect.
func (s *Service) DefaultModel(ctx context.Context) string {
	if s == nil || s.requestPolicy == nil {
		return ""
	}
	return s.requestPolicy.DefaultModel(ctx)
}
