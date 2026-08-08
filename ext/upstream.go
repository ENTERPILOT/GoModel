package ext

import (
	"context"
	"time"
)

// UpstreamCall describes one logical call from GoModel to a configured model
// provider. Transport retries are folded into the same call.
type UpstreamCall struct {
	// Provider is the configured provider instance name. ProviderType is its
	// implementation type (for example "openai" or "anthropic").
	Provider     string
	ProviderType string
	Model        string
	// Operation is the semantic GenAI operation selected by the provider
	// adapter (for example "chat", "generate_content", or "embeddings").
	// It is empty for calls that are not model inference operations.
	Operation string
	Endpoint  string
	Method    string
	Stream    bool
}

// UpstreamResult describes a completed logical provider call. For streaming
// calls completion means that the upstream stream was established, not that
// its response body was fully consumed.
type UpstreamResult struct {
	UpstreamCall
	StatusCode int
	Duration   time.Duration
	Err        error
}

// UpstreamObserver observes calls to model providers without participating in
// request handling. Start may return a derived context (for example one that
// carries a trace span); the same context is passed to End and to the provider
// request. Implementations must be safe for concurrent use and should not
// block the request path.
//
// Core contains observer panics so optional instrumentation cannot fail model
// traffic. Every successful Start invocation is paired with one End call.
type UpstreamObserver interface {
	Name() string
	Start(ctx context.Context, call UpstreamCall) context.Context
	End(ctx context.Context, result UpstreamResult)
}

// UpstreamStreamObserver optionally observes the first response chunk of a
// successful streaming call. Duration is measured from request issuance until
// the first body read that returns bytes; calls that end without bytes are not
// reported.
type UpstreamStreamObserver interface {
	FirstResponseChunk(ctx context.Context, result UpstreamResult)
}
