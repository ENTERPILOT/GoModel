package usage

// Buffer and batch limits for usage tracking.
const (
	// BatchFlushThreshold is the number of entries that triggers an immediate flush.
	// When the batch reaches this size, it's written to storage without waiting for the timer.
	BatchFlushThreshold = 100
)

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
