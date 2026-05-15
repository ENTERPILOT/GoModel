package auditlog

// Buffer and capture limits for audit logging.
const (
	// MaxBodyCapture is the maximum size of request/response bodies to capture (1MB).
	// Prevents memory exhaustion from large payloads.
	MaxBodyCapture = 1024 * 1024

	// MaxContentCapture is the maximum size of accumulated streaming content (1MB).
	// Used by the stream observer to limit reconstructed response body size.
	MaxContentCapture = 1024 * 1024

	// BatchFlushThreshold is the number of entries that triggers an immediate flush.
	// When the batch reaches this size, it's written to storage without waiting for the timer.
	BatchFlushThreshold = 100

	// APIKeyHashPrefixLength is the number of hex characters from SHA256 hash.
	// 16 hex chars = 64 bits of entropy for identification without exposure.
	APIKeyHashPrefixLength = 16
)

// Context keys for storing audit log data in request context.
type contextKey string

const (
	// LogEntryKey is the context key for storing the log entry.
	LogEntryKey contextKey = "auditlog_entry"

	// LogEntryStreamingKey is the context key for marking a request as streaming.
	// When true, the middleware skips logging because the stream observer path
	// handles streaming audit logging.
	LogEntryStreamingKey contextKey = "auditlog_entry_streaming"

	// LogEntryLivePublisherKey stores an optional realtime dashboard publisher.
	LogEntryLivePublisherKey contextKey = "auditlog_live_publisher"
)
