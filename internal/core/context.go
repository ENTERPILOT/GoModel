package core

import "context"

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// RequestIDKey is the context key for the request ID.
	requestIDKey contextKey = "request-id"
	// requestSnapshotKey stores the immutable transport snapshot for the request.
	requestSnapshotKey contextKey = "request-snapshot"
	// whiteBoxPromptKey stores the best-effort semantic extraction for the request.
	whiteBoxPromptKey contextKey = "white-box-prompt"
	// workflowKey stores the request-scoped workflow chosen for handling.
	workflowKey contextKey = "workflow"
	// authKeyIDKey stores the internal managed auth key id for the request.
	authKeyIDKey contextKey = "auth-key-id"
	// effectiveUserPathKey stores a request-scoped user path override applied
	// after ingress capture, for example from a managed auth key.
	effectiveUserPathKey contextKey = "effective-user-path"
	// userPathHeaderNameKey stores the configured request header that carries
	// the user path at the HTTP boundary.
	userPathHeaderNameKey contextKey = "user-path-header-name"
	// batchPreparationMetadataKey stores request-scoped batch preprocessing metadata.
	batchPreparationMetadataKey contextKey = "batch-preparation-metadata"

	// requestLabelsKey stores labels extracted from configured tagging headers.
	requestLabelsKey contextKey = "request-labels"
	// taggingStripHeadersKey stores canonical tagging header names that must not
	// be forwarded to upstream providers.
	taggingStripHeadersKey contextKey = "tagging-strip-headers"

	// enforceReturningUsageDataKey stores whether streaming requests should ask providers
	// to include usage when the provider supports it.
	enforceReturningUsageDataKey contextKey = "enforce-returning-usage-data"

	// guardrailsHashKey stores the SHA-256 hash of the applied guardrail rules
	// for the current request. Set by the translated inference handlers after
	// PatchChatRequest; consumed by the semantic cache to build params_hash.
	guardrailsHashKey contextKey = "guardrails-hash"

	// failoverUsedKey stores whether the translated execution path successfully
	// served the request from a failover model rather than the primary selector.
	// Response cache writers use this to avoid storing failover responses under
	// the primary request key.
	failoverUsedKey contextKey = "failover-used"

	// requestOriginKey stores the logical request origin for internal execution
	// flows that still reuse the translated request pipeline.
	requestOriginKey contextKey = "request-origin"
)

// RequestOrigin identifies whether a request came from an external caller or an
// internal gateway-owned workflow.
type RequestOrigin string

const (
	RequestOriginExternal  RequestOrigin = "external"
	RequestOriginGuardrail RequestOrigin = "guardrail"
)

// WithRequestID returns a new context with the request ID attached.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// GetRequestID retrieves the request ID from the context.
// Returns empty string if not found.
func GetRequestID(ctx context.Context) string {
	if v := ctx.Value(requestIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// WithRequestSnapshot returns a new context with the request snapshot attached.
func WithRequestSnapshot(ctx context.Context, snapshot *RequestSnapshot) context.Context {
	return context.WithValue(ctx, requestSnapshotKey, snapshot)
}

// GetRequestSnapshot retrieves the request snapshot from the context.
func GetRequestSnapshot(ctx context.Context) *RequestSnapshot {
	if v := ctx.Value(requestSnapshotKey); v != nil {
		if snapshot, ok := v.(*RequestSnapshot); ok {
			return snapshot
		}
	}
	return nil
}

// WithWhiteBoxPrompt returns a new context with the white-box prompt attached.
func WithWhiteBoxPrompt(ctx context.Context, prompt *WhiteBoxPrompt) context.Context {
	return context.WithValue(ctx, whiteBoxPromptKey, prompt)
}

// GetWhiteBoxPrompt retrieves the white-box prompt from the context.
func GetWhiteBoxPrompt(ctx context.Context) *WhiteBoxPrompt {
	if v := ctx.Value(whiteBoxPromptKey); v != nil {
		if prompt, ok := v.(*WhiteBoxPrompt); ok {
			return prompt
		}
	}
	return nil
}

// WithWorkflow returns a new context with the workflow attached.
func WithWorkflow(ctx context.Context, workflow *Workflow) context.Context {
	return context.WithValue(ctx, workflowKey, workflow)
}

// GetWorkflow retrieves the workflow from the context.
func GetWorkflow(ctx context.Context) *Workflow {
	if v := ctx.Value(workflowKey); v != nil {
		if workflow, ok := v.(*Workflow); ok {
			return workflow
		}
	}
	return nil
}

// WithAuthKeyID returns a new context with the authenticated managed auth key id attached.
func WithAuthKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, authKeyIDKey, id)
}

// GetAuthKeyID retrieves the managed auth key id from the context.
func GetAuthKeyID(ctx context.Context) string {
	if v := ctx.Value(authKeyIDKey); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// WithEffectiveUserPath returns a new context with an effective user path override attached.
func WithEffectiveUserPath(ctx context.Context, userPath string) context.Context {
	return context.WithValue(ctx, effectiveUserPathKey, userPath)
}

// GetEffectiveUserPath retrieves the effective user path override from context.
func GetEffectiveUserPath(ctx context.Context) string {
	if v := ctx.Value(effectiveUserPathKey); v != nil {
		if userPath, ok := v.(string); ok {
			return userPath
		}
	}
	return ""
}

// WithUserPathHeaderName returns a new context with a non-default configured
// user-path request header name attached. The default header is intentionally a
// no-op and does not clear an existing value.
func WithUserPathHeaderName(ctx context.Context, headerName string) context.Context {
	headerName = UserPathHeaderName(headerName)
	if headerName == UserPathHeader {
		return ctx
	}
	return context.WithValue(ctx, userPathHeaderNameKey, headerName)
}

// WithBatchPreparationMetadata returns a new context with batch preprocessing metadata attached.
func WithBatchPreparationMetadata(ctx context.Context, metadata *BatchPreparationMetadata) context.Context {
	return context.WithValue(ctx, batchPreparationMetadataKey, metadata)
}

// GetBatchPreparationMetadata retrieves batch preprocessing metadata from the context.
func GetBatchPreparationMetadata(ctx context.Context) *BatchPreparationMetadata {
	if v := ctx.Value(batchPreparationMetadataKey); v != nil {
		if metadata, ok := v.(*BatchPreparationMetadata); ok {
			return metadata
		}
	}
	return nil
}

// WithEnforceReturningUsageData returns a new context with the streaming usage policy attached.
func WithEnforceReturningUsageData(ctx context.Context, enforce bool) context.Context {
	return context.WithValue(ctx, enforceReturningUsageDataKey, enforce)
}

// GetEnforceReturningUsageData reports whether the request should ask providers
// to include usage in streaming responses when possible.
func GetEnforceReturningUsageData(ctx context.Context) bool {
	if v := ctx.Value(enforceReturningUsageDataKey); v != nil {
		if enforce, ok := v.(bool); ok {
			return enforce
		}
	}
	return false
}

// WithGuardrailsHash returns a new context with the guardrails hash attached.
// The hash is the SHA-256 of all applied guardrail rule IDs and their versions,
// computed post-patch in the translated inference handlers.
func WithGuardrailsHash(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, guardrailsHashKey, hash)
}

// GetGuardrailsHash retrieves the guardrails hash from the context.
// Returns empty string when no guardrails are active or the hash has not been set.
func GetGuardrailsHash(ctx context.Context) string {
	if v := ctx.Value(guardrailsHashKey); v != nil {
		if h, ok := v.(string); ok {
			return h
		}
	}
	return ""
}

// WithFailoverUsed returns a new context marked as having used a failover model.
func WithFailoverUsed(ctx context.Context) context.Context {
	return context.WithValue(ctx, failoverUsedKey, true)
}

// GetFailoverUsed reports whether the request was served by a failover model.
func GetFailoverUsed(ctx context.Context) bool {
	if v := ctx.Value(failoverUsedKey); v != nil {
		if used, ok := v.(bool); ok {
			return used
		}
	}
	return false
}

// WithRequestOrigin returns a new context with the logical request origin attached.
func WithRequestOrigin(ctx context.Context, origin RequestOrigin) context.Context {
	return context.WithValue(ctx, requestOriginKey, origin)
}

// GetRequestOrigin retrieves the request origin from context.
// When unset, external traffic is assumed.
func GetRequestOrigin(ctx context.Context) RequestOrigin {
	if v := ctx.Value(requestOriginKey); v != nil {
		if origin, ok := v.(RequestOrigin); ok && origin != "" {
			return origin
		}
	}
	return RequestOriginExternal
}
