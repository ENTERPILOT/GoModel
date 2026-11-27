package core

import "context"

// RequestCeilings are the upper bounds the caller's policy puts on every
// provider attempt of a request. The gateway lowers the request to them
// before dispatch; a provider adapter that raises or fills in a value of its
// own (a token limit it defaults, a thinking budget it adds room for, an
// effort it injects) keeps that value within them. Zero values mean no
// ceiling.
type RequestCeilings struct {
	// ReasoningEffort is the highest level of the gateway's effort scale
	// (none, minimal, low, medium, high, xhigh, max) allowed.
	ReasoningEffort string
	// OutputTokens is the highest output token limit allowed.
	OutputTokens int
}

type requestCeilingsKey struct{}

// WithRequestCeilings returns ctx carrying ceilings. A zero value leaves ctx
// unchanged.
func WithRequestCeilings(ctx context.Context, ceilings RequestCeilings) context.Context {
	if ceilings == (RequestCeilings{}) {
		return ctx
	}
	return context.WithValue(ctx, requestCeilingsKey{}, ceilings)
}

// RequestCeilingsFromContext returns the ceilings attached to ctx, or a zero
// value when none are.
func RequestCeilingsFromContext(ctx context.Context) RequestCeilings {
	if ctx == nil {
		return RequestCeilings{}
	}
	ceilings, _ := ctx.Value(requestCeilingsKey{}).(RequestCeilings)
	return ceilings
}
