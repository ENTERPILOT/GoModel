// Package ext is the public extension API for building custom gateway
// binaries on top of GoModel. External modules register request rewriters,
// HTTP middleware, extra routes, runtime settings, and a route selector on a
// Registry (usually ext.Default) before starting the gateway; core consumes
// an immutable snapshot of the registry at server construction. An empty
// registry adds zero request overhead.
package ext

import (
	"context"
	"fmt"
	"net/http"
)

// Endpoint identifies an inference endpoint whose raw JSON body can be
// rewritten before core parses it.
type Endpoint string

// Endpoints eligible for request rewriting. Subroutes (for example
// /v1/messages/count_tokens or /v1/responses/{id}) are never rewritten.
const (
	EndpointChatCompletions Endpoint = "/v1/chat/completions"
	EndpointMessages        Endpoint = "/v1/messages"
	EndpointResponses       Endpoint = "/v1/responses"
)

// Input is the raw inbound request handed to a rewriter before core parses
// it. Body and Header are snapshots owned by the middleware; rewriters must
// treat them as read-only and return new values in Result when changing
// anything.
type Input struct {
	Endpoint Endpoint
	// Body is the raw JSON request body, already bounded by the server's
	// body-size limit.
	Body []byte
	// Header is a clone of the inbound request headers with credential
	// values (Authorization, cookies, API keys, ...) redacted. Rewriters
	// run post-auth; use UserPath for identity.
	Header http.Header
	// UserPath is the canonical authenticated user path, when present.
	UserPath string
	// RequestID is the request correlation ID (X-Request-ID).
	RequestID string
	// SessionID is the detected client session, when present, already scoped
	// by the effective user path. Session detection runs before rewriters, so
	// a rewriter can keep its decisions stable across a conversation.
	SessionID string
}

// Result carries a rewritten body and response-header annotations.
// A nil Result (or nil Body) means the request is unchanged.
type Result struct {
	Body []byte
	// ResponseHeader entries are merged into the HTTP response so rewriters
	// can annotate what they did (for example X-GoModel-Pro-Tokens-Saved).
	ResponseHeader http.Header
	// Detail optionally carries a JSON-serializable summary of what the
	// rewriter changed. It is recorded in the audit trail's request-revision
	// chain and never sent upstream; it must never contain secrets or
	// request credentials.
	Detail any
	// TokensSaved is the rewriter's estimate of prompt tokens its body
	// change removed from the request. When positive and the rewritten body
	// is applied, core adds it to the request's usage record together with
	// the input cost those tokens would have incurred, and the dashboard
	// aggregates both as rewrite savings. Leave zero when the rewrite does
	// not shrink the prompt.
	TokensSaved int
}

// RequestRewriter rewrites raw JSON request bodies at ingress, after
// authentication and before model resolution, so body changes (including the
// "model" field) affect routing, failover, guardrails, budgets, and caching.
//
// Rewriters run once per request in registration order; each receives the
// previous rewriter's output. Implementations must be safe for concurrent
// use. Errors fail the request (fail-closed): return a *RejectionError for a
// client-visible status, any other error maps to HTTP 500.
type RequestRewriter interface {
	Name() string
	Rewrite(ctx context.Context, in Input) (*Result, error)
}

// ResponseFeedbackObserver receives content-free feedback after a rewritten
// request successfully reaches a provider. It is an optional companion to
// RequestRewriter: core detects implementations structurally and invokes them
// for both ordinary and streaming responses. usageObserved distinguishes a
// confirmed zero from a provider/stream that returned no usage breakdown.
// Implementations must be safe for concurrent use and return promptly.
//
// The flat signature intentionally uses only long-standing extension types so
// extensions can implement the hook while supporting older core releases;
// older cores simply never call it.
type ResponseFeedbackObserver interface {
	ObserveResponse(
		ctx context.Context,
		requestID string,
		endpoint Endpoint,
		sessionID string,
		model string,
		providerType string,
		providerName string,
		inputTokens int,
		cachedInputTokens int,
		cacheWriteInputTokens int,
		usageObserved bool,
	)
}

// ResponseFeedbackFilter lets an observer decline feedback per request. Core
// calls it after Rewrite and registers the observer only when it returns true.
// Observers without this optional interface receive every successful response.
type ResponseFeedbackFilter interface {
	WantsResponseFeedback(in Input, result *Result) bool
}

// SettingOption is one allowed value for a dashboard-editable extension
// setting. Label and Description are safe to expose in the admin UI.
type SettingOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SettingDescriptor describes one deployment-wide extension setting.
// Locked settings are controlled by an environment variable and remain
// visible, but cannot be changed through the admin API. Options must list
// every accepted value for an unlocked setting, including Value; registration
// fails when that contract is not met.
type SettingDescriptor struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Value       string          `json:"value"`
	Locked      bool            `json:"locked"`
	ManagedBy   string          `json:"managed_by,omitempty"`
	Options     []SettingOption `json:"options"`
}

// RuntimeSetting is a mutable, deployment-wide extension setting. Core owns
// persistence and the admin API; the extension owns validation and applying
// the value to its live runtime state. Apply receives only a value advertised
// in Descriptor.Options. Implementations must be safe for concurrent use.
type RuntimeSetting interface {
	Descriptor() SettingDescriptor
	Apply(value string) error
}

// RejectionError rejects the request with a client-visible status code and
// machine-readable error code, rendered in the endpoint's native error
// dialect (OpenAI or Anthropic envelope).
type RejectionError struct {
	Status  int
	Code    string
	Message string
}

func (e *RejectionError) Error() string {
	return fmt.Sprintf("request rejected (%d %s): %s", e.Status, e.Code, e.Message)
}
