package pluginapi

import "time"

// Meta is a read-only snapshot of request identity and routing facts. Fields
// that are not known yet in a phase are empty (for example the resolved
// Provider and Model during [RequestHook]).
type Meta struct {
	// RequestID is the gateway request identifier.
	RequestID string
	// Dialect is the client API dialect: "openai" or "anthropic_messages".
	Dialect string
	// Endpoint is the request URL path, for example "/v1/chat/completions".
	Endpoint string
	// Operation is the gateway operation name, for example "chat_completions".
	Operation string

	// UserPath is the effective user path the request is scoped to.
	UserPath string
	// AuthKeyID identifies the API key used, never the key itself.
	AuthKeyID string
	// Labels are the request labels attached by tagging rules.
	Labels map[string]string
	// SessionID is the detected conversation session, if any.
	SessionID string

	// RequestedModel is the model the client asked for.
	RequestedModel string
	// Provider is the resolved provider type, for example "anthropic".
	Provider string
	// ProviderName is the resolved provider instance name.
	ProviderName string
	// Model is the resolved provider model.
	Model string
	// VirtualModelSource is the virtual model that produced the resolution,
	// empty when the client addressed a provider model directly.
	VirtualModelSource string

	// WorkflowVersionID identifies the workflow version in effect.
	WorkflowVersionID string
	// Features lists workflow feature flags in effect.
	Features map[string]bool
	// Stream reports whether the client asked for a streaming response.
	Stream bool

	// Attempts lists provider attempts made so far (response phases only).
	Attempts []Attempt
	// Cache describes prompt-cache planning and session affinity.
	Cache CacheInfo
	// Origin says who issued the request: "client", "plugin", or another
	// gateway-internal source.
	Origin string
}

// Attempt is one provider call made for the request.
type Attempt struct {
	// Seq is the attempt number, starting at 1.
	Seq int
	// Kind is the attempt kind, for example "primary" or "failover".
	Kind string
	// Provider is the provider type; ProviderName the instance; Model the
	// provider model.
	Provider, ProviderName, Model string
	// StatusCode is the upstream HTTP status, when known.
	StatusCode int
	// Success reports whether the attempt produced a usable response.
	Success bool
	// ErrorCode is the gateway error code when the attempt failed.
	ErrorCode string
	// Duration is the wall-clock time of the attempt.
	Duration time.Duration
}

// CacheInfo exposes prompt-cache planning so plugins can avoid breaking it.
type CacheInfo struct {
	// PlannedPrefixMessages is how many leading messages the provider cache
	// planner will mark as the cached prefix. Editing a message with a lower
	// index invalidates the cache for the session; appending does not.
	PlannedPrefixMessages int
	// SessionTarget is the sticky "provider/model" for the session, if any.
	SessionTarget string
}
