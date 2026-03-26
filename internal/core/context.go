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
	// executionPlanKey stores the request-scoped execution plan chosen for handling.
	executionPlanKey contextKey = "execution-plan"
	// batchPreparationMetadataKey stores request-scoped batch preprocessing metadata.
	batchPreparationMetadataKey contextKey = "batch-preparation-metadata"

	// enforceReturningUsageDataKey stores whether streaming requests should ask providers
	// to include usage when the provider supports it.
	enforceReturningUsageDataKey contextKey = "enforce-returning-usage-data"

	// guardrailsHashKey stores the SHA-256 hash of the applied guardrail rules
	// for the current request. Set by the translated inference handlers after
	// PatchChatRequest; consumed by the semantic cache to build params_hash.
	guardrailsHashKey contextKey = "guardrails-hash"

	// fallbackUsedKey stores whether the translated execution path successfully
	// served the request from a fallback model rather than the primary selector.
	// Response cache writers use this to avoid storing fallback responses under
	// the primary request key.
	fallbackUsedKey contextKey = "fallback-used"
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

// WithExecutionPlan returns a new context with the execution plan attached.
func WithExecutionPlan(ctx context.Context, plan *ExecutionPlan) context.Context {
	return context.WithValue(ctx, executionPlanKey, plan)
}

// GetExecutionPlan retrieves the execution plan from the context.
func GetExecutionPlan(ctx context.Context) *ExecutionPlan {
	if v := ctx.Value(executionPlanKey); v != nil {
		if plan, ok := v.(*ExecutionPlan); ok {
			return plan
		}
	}
	return nil
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

// WithFallbackUsed returns a new context marked as having used a fallback model.
func WithFallbackUsed(ctx context.Context) context.Context {
	return context.WithValue(ctx, fallbackUsedKey, true)
}

// GetFallbackUsed reports whether the request was served by a fallback model.
func GetFallbackUsed(ctx context.Context) bool {
	if v := ctx.Value(fallbackUsedKey); v != nil {
		if used, ok := v.(bool); ok {
			return used
		}
	}
	return false
}
