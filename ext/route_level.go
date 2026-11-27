package ext

import "github.com/enterpilot/gomodel/internal/core"

// Route levels say how capable a model a request needs. A virtual model
// using the "auto" strategy labels each target with one, and a request
// rewriter names the lowest level that may serve a request in
// Result.RouteLevel. They are ordered: light < normal < smart < smartest.
const (
	RouteLevelLight    = core.RouteLevelLight
	RouteLevelNormal   = core.RouteLevelNormal
	RouteLevelSmart    = core.RouteLevelSmart
	RouteLevelSmartest = core.RouteLevelSmartest
)

// RouteLevels lists the route levels, lowest first.
func RouteLevels() []string { return core.RouteLevels() }

// RouteLevelRank returns the position of level in the order, starting at 1,
// or 0 when level is not a route level.
func RouteLevelRank(level string) int { return core.RouteLevelRank(level) }

// AutoRoute describes a virtual model that uses the "auto" strategy.
type AutoRoute struct {
	// Source is the virtual model's name. It differs from the requested
	// model when the request named an alias of the route.
	Source string
	// Levels are the distinct levels of the route's targets, lowest first.
	// They describe the configuration: a target that is unavailable right
	// now, or that the caller may not use, still counts.
	Levels []string
}

// AutoRoutes tells which requested models route by level.
type AutoRoutes interface {
	// AutoRoute returns the auto route a request for model by userPath
	// resolves to: a virtual model using the "auto" strategy, named directly
	// or through single-target aliases. It reports false for every other
	// model, including a route scoped to user paths the caller is outside
	// of.
	AutoRoute(model, userPath string) (AutoRoute, bool)
}

// AutoRoutesAware is optionally implemented by a request rewriter that sets
// Result.RouteLevel, such as a router that classifies how hard a request is.
// Core installs the lookup before serving and installs a fresh one for every
// configuration generation, so implementations must swap it atomically.
// Until one is installed, a rewriter must treat every model as not routed.
type AutoRoutesAware interface {
	SetAutoRoutes(AutoRoutes)
}
