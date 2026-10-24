package pluginapi

import (
	"encoding/json"
	"time"
)

// RouteCandidate is one target a virtual model may route to.
type RouteCandidate struct {
	// Provider is the provider name and Model the provider model; Qualified
	// is "provider/model".
	Provider, Model, Qualified string
	// Weight is the configured weight; zero when the route has none.
	Weight float64
	// InputPerMtok and OutputPerMtok are prices per million tokens, when
	// known.
	InputPerMtok, OutputPerMtok *float64
}

// RouteRequest is what a [RouteStrategy] selects from.
type RouteRequest struct {
	// Source is the virtual model being routed.
	Source string
	// SessionID is the detected session; SessionTarget the sticky
	// "provider/model" for it, when one exists.
	SessionID, SessionTarget string
	// Candidates are the eligible targets, in configured order.
	Candidates []RouteCandidate
	Meta       Meta
	// Prompt is the unified request; nil when the operation has none.
	Prompt *Prompt
	// Config is the virtual model's strategy_config as JSON.
	Config json.RawMessage
}

// RouteChoice is the target picked by [RouteStrategy.Select].
type RouteChoice struct {
	// Qualified is the chosen candidate's "provider/model".
	Qualified string
	// Reason is recorded in the audit trail.
	Reason string
}

// RouteTarget identifies a provider model.
type RouteTarget struct {
	Provider, Model string
}

// Qualified returns "provider/model".
func (t RouteTarget) Qualified() string {
	return t.Provider + "/" + t.Model
}

// RouteOutcome reports how an attempt at a routed target went.
type RouteOutcome struct {
	Source  string
	Target  RouteTarget
	Success bool
	// StatusCode is the upstream HTTP status, when known.
	StatusCode int
	Latency    time.Duration
	// Timeout reports that the attempt hit the provider timeout.
	Timeout bool
}
