package ext

import "time"

// RouteCandidate is one currently viable target of a load-balanced virtual
// model, offered to a RouteSelector. Pricing comes from the model registry
// and is per million tokens; nil means the registry has no price for the
// target.
type RouteCandidate struct {
	// Provider is the configured provider name (e.g. "openai", "azure-eu").
	Provider string
	// Model is the provider-native model ID (e.g. "gpt-4o").
	Model string
	// Qualified is "provider/model", the stable key selection answers with.
	Qualified string
	// Weight is the operator-configured target weight; 0 means unset (treat
	// as 1).
	Weight        float64
	InputPerMtok  *float64
	OutputPerMtok *float64
}

// RouteRequest asks a RouteSelector to pick one target for a request routed
// through a load-balanced virtual model. Candidates are the targets that are
// catalog-supported and have rate-limit capacity right now, in declared
// order; there are always at least two (single-candidate picks bypass the
// selector so an alias behaves identically with and without one).
type RouteRequest struct {
	// Source is the virtual model name the request addressed.
	Source string
	// SessionID is the detected client session, when present. Session
	// affinity is enforced by core before the selector runs; the ID is
	// provided for observability only.
	SessionID  string
	Candidates []RouteCandidate
}

// RouteTarget identifies a provider/model pair as seen by the upstream
// client layer.
type RouteTarget struct {
	Provider string
	Model    string
}

// Qualified returns the "provider/model" key matching RouteCandidate.Qualified.
func (t RouteTarget) Qualified() string { return t.Provider + "/" + t.Model }

// RouteOutcome describes one completed upstream attempt. Every attempt is
// reported — primaries, retries, and failover attempts alike — so selectors
// learn from traffic they did not steer.
type RouteOutcome struct {
	RouteTarget
	// Endpoint is the upstream API endpoint (e.g. "/chat/completions").
	Endpoint string
	// StatusCode is the upstream HTTP status; 0 on a network error.
	StatusCode int
	// Duration is the attempt duration. For streaming requests it measures
	// time to stream establishment, not the full stream lifetime.
	Duration time.Duration
	Stream   bool
	// Err is the client-layer error, nil on success.
	Err error
}

// RouteSelector steers load balancing for virtual models using the
// "adaptive" strategy. Core consults the selector only to pick among
// currently viable targets; session affinity, rate-limit capacity, failover
// chains, and retries all remain core's responsibility.
//
// Select must be fast and must not block: it runs on the request path before
// the upstream call. Implementations must be safe for concurrent use. A
// (_, false) answer — and any answer naming a model outside Candidates —
// falls back to weighted round robin, so selectors fail open by declining.
//
// OnAttemptStart and OnAttemptEnd observe the upstream client lifecycle for
// every attempt. For streaming requests OnAttemptEnd fires when the stream is
// established, not when it closes.
type RouteSelector interface {
	Name() string
	Select(req RouteRequest) (qualified string, ok bool)
	OnAttemptStart(target RouteTarget)
	OnAttemptEnd(outcome RouteOutcome)
}
