package usage

// Context keys for storing usage data in request context.
type contextKey string

const (
	// UsageEntryKey is the context key for storing the usage entry.
	UsageEntryKey contextKey = "usage_entry"

	// UsageEntryStreamingKey is the context key for marking a request as streaming.
	// When true, the middleware skips logging because streaming usage is handled
	// by the shared SSE observer path.
	UsageEntryStreamingKey contextKey = "usage_entry_streaming"
)
