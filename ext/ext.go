// Package ext is the public extension API for building custom gateway
// binaries on top of GoModel. External modules register request rewriters,
// HTTP middleware, and extra routes on a Registry (usually ext.Default)
// before starting the gateway; core consumes an immutable snapshot of the
// registry at server construction. An empty registry adds zero request
// overhead.
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
