package core

import "context"

// requestScopeKey stores the mutable per-request scope installed at ingress.
const requestScopeKey contextKey = "request-scope"

// RequestScope holds the per-request values that the HTTP middleware chain
// fills in as a request moves through ingress capture, tagging, auth, session
// detection and workflow resolution. It is installed once by the ingress
// middleware and mutated in place afterwards, so the chain does not re-wrap
// the request context (and copy *http.Request) at every stage.
//
// The With* accessors keep context-value semantics: on a context that carries
// a scope they clone it and layer the clone, so a context derived before the
// call never observes the change. Only the Set* methods mutate in place; the
// middleware chain uses them on the request goroutine before any concurrent
// work starts.
type RequestScope struct {
	requestID               string
	snapshot                *RequestSnapshot
	whiteBoxPrompt          *WhiteBoxPrompt
	workflow                *Workflow
	sessionID               string
	authKeyID               string
	credentialAllowedModels []string
	effectiveUserPath       string
	userPathHeaderName      string
	labels                  []string
	taggingStripHeaders     map[string]struct{}
	rewriteTokensSaved      int
	failoverUsed            bool
}

// WithRequestScope installs a request scope on ctx and returns it. The scope
// is seeded from the values ctx already carries, so readers see exactly what
// they would have seen before the scope existed. When ctx already carries a
// scope it is returned unchanged.
func WithRequestScope(ctx context.Context) (context.Context, *RequestScope) {
	if scope := RequestScopeFromContext(ctx); scope != nil {
		return ctx, scope
	}
	scope := &RequestScope{
		requestID:               GetRequestID(ctx),
		snapshot:                GetRequestSnapshot(ctx),
		whiteBoxPrompt:          GetWhiteBoxPrompt(ctx),
		workflow:                GetWorkflow(ctx),
		sessionID:               SessionIDFromContext(ctx),
		authKeyID:               GetAuthKeyID(ctx),
		credentialAllowedModels: GetCredentialAllowedModels(ctx),
		effectiveUserPath:       GetEffectiveUserPath(ctx),
		userPathHeaderName:      userPathHeaderNameValue(ctx),
		labels:                  RequestLabelsFromContext(ctx),
		taggingStripHeaders:     TaggingStripHeadersFromContext(ctx),
		rewriteTokensSaved:      RewriteTokensSavedFromContext(ctx),
		failoverUsed:            GetFailoverUsed(ctx),
	}
	return context.WithValue(ctx, requestScopeKey, scope), scope
}

// RequestScopeFromContext returns the request scope carried by ctx, or nil.
func RequestScopeFromContext(ctx context.Context) *RequestScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(requestScopeKey).(*RequestScope)
	return scope
}

// withScope applies update to a clone of the scope carried by ctx and layers
// the clone on top, preserving context-value semantics. It reports false when
// ctx carries no scope so the caller can fall back to a plain context value.
func withScope(ctx context.Context, update func(*RequestScope)) (context.Context, bool) {
	scope := RequestScopeFromContext(ctx)
	if scope == nil {
		return ctx, false
	}
	clone := *scope
	update(&clone)
	return context.WithValue(ctx, requestScopeKey, &clone), true
}

// SetRequestID records the gateway request id.
func (s *RequestScope) SetRequestID(requestID string) { s.requestID = requestID }

// SetSnapshot replaces the transport snapshot.
func (s *RequestScope) SetSnapshot(snapshot *RequestSnapshot) { s.snapshot = snapshot }

// SetWhiteBoxPrompt replaces the semantic extraction.
func (s *RequestScope) SetWhiteBoxPrompt(prompt *WhiteBoxPrompt) { s.whiteBoxPrompt = prompt }

// SetWorkflow replaces the request workflow.
func (s *RequestScope) SetWorkflow(workflow *Workflow) { s.workflow = workflow }

// SetSessionID records the detected client session id. An empty id is ignored,
// matching WithSessionID.
func (s *RequestScope) SetSessionID(sessionID string) {
	if sessionID == "" {
		return
	}
	s.sessionID = sessionID
}

// SetAuthKeyID records the managed auth key id.
func (s *RequestScope) SetAuthKeyID(id string) { s.authKeyID = id }

// SetCredentialAllowedModels replaces the credential-bound model allowlist.
// An empty list clears it, matching WithCredentialAllowedModels.
func (s *RequestScope) SetCredentialAllowedModels(allowed []string) {
	if len(allowed) == 0 {
		allowed = nil
	}
	s.credentialAllowedModels = allowed
}

// SetEffectiveUserPath replaces the effective user path override. An empty
// value clears it, matching WithEffectiveUserPath.
func (s *RequestScope) SetEffectiveUserPath(userPath string) { s.effectiveUserPath = userPath }

// SetUserPathHeaderName records a non-default user-path header name. The
// default header is a no-op, matching WithUserPathHeaderName.
func (s *RequestScope) SetUserPathHeaderName(headerName string) {
	headerName = UserPathHeaderName(headerName)
	if headerName == UserPathHeader {
		return
	}
	s.userPathHeaderName = headerName
}

// SetLabels replaces the request labels. An empty list is ignored, matching
// WithRequestLabels.
func (s *RequestScope) SetLabels(labels []string) {
	if len(labels) == 0 {
		return
	}
	s.labels = labels
}

// SetTaggingStripHeaders replaces the do-not-pass header set. An empty set is
// ignored, matching WithTaggingStripHeaders.
func (s *RequestScope) SetTaggingStripHeaders(headers map[string]struct{}) {
	if len(headers) == 0 {
		return
	}
	s.taggingStripHeaders = headers
}

// SetRewriteTokensSaved records the rewrite savings estimate. Non-positive
// totals are ignored, matching WithRewriteTokensSaved.
func (s *RequestScope) SetRewriteTokensSaved(tokensSaved int) {
	if tokensSaved <= 0 {
		return
	}
	s.rewriteTokensSaved = tokensSaved
}

// SetFailoverUsed marks the request as served by a failover model.
func (s *RequestScope) SetFailoverUsed() { s.failoverUsed = true }
