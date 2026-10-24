package pluginapi

import (
	"context"
	"encoding/json"
)

// Plugin is the base interface every plugin implements. Hooks are optional
// interfaces detected by type assertion on the same value.
type Plugin interface {
	// Manifest describes the plugin. It must be cheap and side-effect free.
	Manifest() Manifest
	// Init receives the instance configuration (validated against
	// Manifest.ConfigSchema) and a Host for logging, metrics, and internal
	// inference. It is called once per configured instance, at startup and
	// again when an operator updates the instance.
	Init(ctx context.Context, config json.RawMessage, host Host) error
	// Close releases resources. It is called when the instance is removed or
	// replaced.
	Close(ctx context.Context) error
}

// RequestHook runs after authentication and session detection, before model
// resolution. Edits made to x.Prompt affect routing; Meta routing fields are
// still empty at this point.
type RequestHook interface {
	OnRequest(ctx context.Context, x *Exchange) (Decision, error)
}

// PromptHook is the guardrails phase: it runs after routing and before the
// provider call, with the resolved provider and model in x.Meta. It may edit
// x.Prompt, block the request, or answer it with a [Respond] decision.
type PromptHook interface {
	OnPrompt(ctx context.Context, x *Exchange) (Decision, error)
}

// ResponseHook runs on a complete non-streaming response, or on the assembled
// response of a buffered stream, before it is sent to the client. It may edit
// x.Response.
type ResponseHook interface {
	OnResponse(ctx context.Context, x *Exchange) (Decision, error)
}

// StreamHook runs per parsed stream event. StreamPolicy tells GoModel how to
// drive the hook (observe only, transform events in flight, or buffer the
// whole stream). OnStreamEnd runs once after the last event.
type StreamHook interface {
	StreamPolicy() StreamPolicy
	OnStreamEvent(ctx context.Context, x *Exchange, ev *StreamEvent) (StreamDecision, error)
	OnStreamEnd(ctx context.Context, x *Exchange) (Decision, error)
}

// RouteStrategy is a load-balancing strategy for virtual models. Select picks
// one of the candidates; OnAttemptEnd reports how the attempt went so the
// strategy can adapt.
type RouteStrategy interface {
	Select(ctx context.Context, req RouteRequest) (RouteChoice, error)
	OnAttemptEnd(outcome RouteOutcome)
}

// CompleteHook runs after the client response is fully written. It never
// blocks the client; panics and slow calls are logged only.
type CompleteHook interface {
	OnComplete(ctx context.Context, x *Exchange)
}

// HealthChecker is implemented by plugins whose instances depend on
// something outside the process: a sidecar, a remote classifier, a policy
// service. GoModel calls Health off the request path, once an instance is
// built and again on every guardrail refresh (one minute by default), with
// a short deadline. A non-nil error marks the instance degraded in the
// admin views and the dashboard, with the error text as the reason. That
// text is shown to operators and logged, so like [Decision.Detail] it must
// not contain secrets; it is truncated to a few hundred characters. Health
// never changes how traffic is handled: fail_mode decides what a failing
// hook does. Plugins without external dependencies need not implement it.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// ContentEditor is implemented by a plugin whose manifest declares Mutates
// but whose configuration decides whether it actually edits content: a
// presidio instance that only flags detections, a string_replace instance
// that only blocks. GoModel asks a configured instance before work it does
// solely so an editing plugin sees the whole request — replaying the stored
// history of a chained Responses request instead of letting the provider
// resolve previous_response_id itself. It never relaxes how a hook runs.
// A mutating plugin that does not implement it is taken to edit.
type ContentEditor interface {
	EditsContent() bool
}
