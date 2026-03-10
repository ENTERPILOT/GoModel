package core

import "context"

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// RequestIDKey is the context key for the request ID.
	requestIDKey contextKey = "request-id"
	// ingressFrameKey stores the immutable ingress capture for the request.
	ingressFrameKey contextKey = "ingress-frame"
	// semanticEnvelopeKey stores the best-effort semantic extraction for the request.
	semanticEnvelopeKey contextKey = "semantic-envelope"
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

// WithIngressFrame returns a new context with the ingress frame attached.
func WithIngressFrame(ctx context.Context, frame *IngressFrame) context.Context {
	return context.WithValue(ctx, ingressFrameKey, frame)
}

// GetIngressFrame retrieves the ingress frame from the context.
func GetIngressFrame(ctx context.Context) *IngressFrame {
	if v := ctx.Value(ingressFrameKey); v != nil {
		if frame, ok := v.(*IngressFrame); ok {
			return frame
		}
	}
	return nil
}

// WithSemanticEnvelope returns a new context with the semantic envelope attached.
func WithSemanticEnvelope(ctx context.Context, env *SemanticEnvelope) context.Context {
	return context.WithValue(ctx, semanticEnvelopeKey, env)
}

// GetSemanticEnvelope retrieves the semantic envelope from the context.
func GetSemanticEnvelope(ctx context.Context) *SemanticEnvelope {
	if v := ctx.Value(semanticEnvelopeKey); v != nil {
		if env, ok := v.(*SemanticEnvelope); ok {
			return env
		}
	}
	return nil
}
