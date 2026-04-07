// Package usage provides token usage tracking for the AI gateway.
// It captures detailed token usage from API responses and stores them for analytics.
package usage

import (
	"context"
	"time"
)

// UsageStore defines the interface for usage storage backends.
// Implementations must be safe for concurrent use.
type UsageStore interface {
	// WriteBatch writes multiple usage entries to storage.
	// This is called by the Logger when flushing buffered entries.
	WriteBatch(ctx context.Context, entries []*UsageEntry) error

	// Flush forces any pending writes to complete.
	// Called during graceful shutdown.
	Flush(ctx context.Context) error

	// Close releases resources and flushes pending writes.
	Close() error
}

// UsageEntry represents a single token usage record.
type UsageEntry struct {
	// ID is a unique identifier for this usage entry (UUID)
	ID string `json:"id" bson:"_id"`

	// RequestID links to the audit log entry (from X-Request-ID header)
	RequestID string `json:"request_id" bson:"request_id"`

	// ProviderID is the provider's response ID (e.g., "chatcmpl-abc123", "msg_xyz")
	ProviderID string `json:"provider_id" bson:"provider_id"`

	// Timestamp is when the request completed
	Timestamp time.Time `json:"timestamp" bson:"timestamp"`

	// Request context
	Model        string `json:"model" bson:"model"`
	Provider     string `json:"provider" bson:"provider"` // canonical provider type used for routing, filters, and pricing
	ProviderName string `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Endpoint     string `json:"endpoint" bson:"endpoint"`
	UserPath     string `json:"user_path,omitempty" bson:"user_path,omitempty"`
	CacheType    string `json:"cache_type,omitempty" bson:"cache_type,omitempty"`

	// Standard token counts (normalized across providers)
	InputTokens  int `json:"input_tokens" bson:"input_tokens"`
	OutputTokens int `json:"output_tokens" bson:"output_tokens"`
	TotalTokens  int `json:"total_tokens" bson:"total_tokens"`

	// RawData contains provider-specific extended usage data (JSONB)
	// Examples:
	//   OpenAI: {"cached_tokens": 100, "reasoning_tokens": 50}
	//   Anthropic: {"cache_creation_input_tokens": 200, "cache_read_input_tokens": 150}
	//   Gemini: {"cached_tokens": 100, "thought_tokens": 75, "tool_use_tokens": 25}
	RawData map[string]any `json:"raw_data,omitempty" bson:"raw_data,omitempty"`

	// Cost fields (nil = unknown/model not in list, 0.0 = free)
	InputCost  *float64 `json:"input_cost,omitempty" bson:"input_cost,omitempty"`
	OutputCost *float64 `json:"output_cost,omitempty" bson:"output_cost,omitempty"`
	TotalCost  *float64 `json:"total_cost,omitempty" bson:"total_cost,omitempty"`

	// CostsCalculationCaveat describes any incomplete aspects of cost calculation.
	// Empty means all token types were fully mapped to pricing data.
	CostsCalculationCaveat string `json:"costs_calculation_caveat,omitempty" bson:"costs_calculation_caveat,omitempty"`
}

// Config holds usage tracking configuration
type Config struct {
	// Enabled controls whether usage tracking is active
	Enabled bool

	// EnforceReturningUsageData controls whether to ask streaming providers to return usage data when possible.
	// When true, stream_options: {"include_usage": true} is added for provider paths that support it.
	// Default: true
	EnforceReturningUsageData bool

	// BufferSize is the number of usage entries to buffer before flushing
	BufferSize int

	// FlushInterval is how often to flush buffered entries
	FlushInterval time.Duration

	// RetentionDays is how long to keep usage data (0 = forever)
	RetentionDays int
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Enabled:                   false,
		EnforceReturningUsageData: true,
		BufferSize:                1000,
		FlushInterval:             5 * time.Second,
		RetentionDays:             90,
	}
}
