package ext

import "context"

// HealthStatus is the state an extension reports for one of its dependencies.
type HealthStatus string

// Health statuses, matching the component values of GET /health/ready.
const (
	// HealthOK means the dependency works normally.
	HealthOK HealthStatus = "ok"
	// HealthDegraded means the extension still serves requests, for example
	// from stale cached values. Readiness reports degraded with HTTP 200, so
	// the instance stays in rotation.
	HealthDegraded HealthStatus = "degraded"
	// HealthDown means the extension cannot serve requests. Readiness reports
	// not_ready with HTTP 503, taking the instance out of rotation.
	HealthDown HealthStatus = "down"
)

// HealthChecker contributes a component to GET /health/ready. Liveness
// (GET /health) never consults it, so a failing dependency never restarts the
// process.
//
// Name is the component key in the readiness response; it must be non-empty
// and must not collide with a core component ("storage", "cache", "models") or
// another checker. CheckHealth runs on every readiness probe under a short
// timeout, so it should report recently observed state rather than call slow
// remote services. An unrecognized status is reported as degraded, as is a
// check that panics or does not return before the deadline; core runs at most
// one call per checker at a time. Implementations must be safe for concurrent
// use.
type HealthChecker interface {
	Name() string
	CheckHealth(ctx context.Context) HealthStatus
}
