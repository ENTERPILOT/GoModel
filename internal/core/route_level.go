package core

import (
	"context"
	"strings"
)

// Route levels name how capable a model a request needs, and how capable a
// target of a virtual model with the "auto" strategy is. They are ordered:
// light < normal < smart < smartest.
const (
	RouteLevelLight    = "light"
	RouteLevelNormal   = "normal"
	RouteLevelSmart    = "smart"
	RouteLevelSmartest = "smartest"
)

// routeLevels lists the route levels, lowest first.
var routeLevels = [...]string{RouteLevelLight, RouteLevelNormal, RouteLevelSmart, RouteLevelSmartest}

// RouteLevels lists the route levels, lowest first.
func RouteLevels() []string {
	return append([]string(nil), routeLevels[:]...)
}

// RouteLevelName returns the route level with the given rank (see
// RouteLevelRank), or "" when rank is not one.
func RouteLevelName(rank int) string {
	if rank < 1 || rank > len(routeLevels) {
		return ""
	}
	return routeLevels[rank-1]
}

// RouteLevelRank returns the position of level in the order, starting at 1,
// or 0 when level is not a route level. Case and surrounding space are
// ignored.
func RouteLevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case RouteLevelLight:
		return 1
	case RouteLevelNormal:
		return 2
	case RouteLevelSmart:
		return 3
	case RouteLevelSmartest:
		return 4
	default:
		return 0
	}
}

// WithRouteLevel returns a context carrying the lowest route level that may
// serve the request. A value that is not a route level leaves the context
// unchanged.
func WithRouteLevel(ctx context.Context, level string) context.Context {
	level = strings.ToLower(strings.TrimSpace(level))
	if RouteLevelRank(level) == 0 {
		return ctx
	}
	return context.WithValue(ctx, routeLevelKey, level)
}

// RouteLevelFromContext returns the lowest route level that may serve the
// request, or "" when nothing set one.
func RouteLevelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	level, _ := ctx.Value(routeLevelKey).(string)
	return level
}

// WithoutRouteLevel returns a context that carries no route level. Calls the
// gateway makes on a request's behalf (a guardrail's or a plugin's own model
// call) use it, so they do not inherit the level worked out for the request
// that triggered them.
func WithoutRouteLevel(ctx context.Context) context.Context {
	if RouteLevelFromContext(ctx) == "" {
		return ctx
	}
	return context.WithValue(ctx, routeLevelKey, "")
}

// ServedRouteLevel receives the level a virtual model using the "auto"
// strategy chose while a request's model is resolved.
type ServedRouteLevel struct {
	// Level is the chosen level, or "" when no auto route took part.
	Level string
}

// WithServedRouteLevel returns a context in which model resolution reports
// the level it chose, and the value it reports into.
func WithServedRouteLevel(ctx context.Context) (context.Context, *ServedRouteLevel) {
	served := &ServedRouteLevel{}
	return context.WithValue(ctx, servedRouteLevelKey, served), served
}

// ReportServedRouteLevel records the level an auto route chose, when the
// caller asked for it with WithServedRouteLevel.
func ReportServedRouteLevel(ctx context.Context, rank int) {
	if ctx == nil {
		return
	}
	if served, ok := ctx.Value(servedRouteLevelKey).(*ServedRouteLevel); ok && served != nil {
		served.Level = RouteLevelName(rank)
	}
}
