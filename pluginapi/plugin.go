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
