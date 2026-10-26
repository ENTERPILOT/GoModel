package core

import "context"

// VirtualModelRoute receives the virtual models one model resolution routed
// through. The gateway installs one around resolving a request's model, so
// the route a load balancer actually took reaches RequestModelResolution even
// where several paths lead to the same concrete model.
type VirtualModelRoute struct {
	path []string
}

type virtualModelRouteKey struct{}

// WithVirtualModelRoute returns ctx carrying a fresh route recorder.
func WithVirtualModelRoute(ctx context.Context) (context.Context, *VirtualModelRoute) {
	route := &VirtualModelRoute{}
	return context.WithValue(ctx, virtualModelRouteKey{}, route), route
}

// RecordVirtualModelRoute stores path, outermost virtual model first, as the
// route of the latest resolution. It does nothing when ctx has no recorder.
func RecordVirtualModelRoute(ctx context.Context, path []string) {
	if route, ok := ctx.Value(virtualModelRouteKey{}).(*VirtualModelRoute); ok {
		route.path = path
	}
}

// Path returns the recorded route, or nil when none was recorded.
func (r *VirtualModelRoute) Path() []string {
	if r == nil {
		return nil
	}
	return r.path
}
