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
