package core

import "context"

// WithRequestLabels returns a new context with the request labels attached.
// Labels are extracted at ingress from configured tagging headers.
func WithRequestLabels(ctx context.Context, labels []string) context.Context {
	if len(labels) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestLabelsKey, labels)
}

// RequestLabelsFromContext returns the labels extracted for this request.
// Callers must treat the returned slice as read-only.
func RequestLabelsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if labels, ok := ctx.Value(requestLabelsKey).([]string); ok {
		return labels
	}
	return nil
}

// WithTaggingStripHeaders returns a new context carrying the canonical tagging
// header names that must not be forwarded to upstream providers.
func WithTaggingStripHeaders(ctx context.Context, headers map[string]struct{}) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, taggingStripHeadersKey, headers)
}

// TaggingStripHeadersFromContext returns the canonical header names marked as
// do-not-pass by the tagging configuration. Callers must treat the returned
// map as read-only.
func TaggingStripHeadersFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	if headers, ok := ctx.Value(taggingStripHeadersKey).(map[string]struct{}); ok {
		return headers
	}
	return nil
}
