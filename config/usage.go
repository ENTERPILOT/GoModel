package config

// UsageConfig holds token usage tracking configuration
type UsageConfig struct {
	// Enabled controls whether usage tracking is active
	// Default: true
	Enabled bool `yaml:"enabled" env:"USAGE_ENABLED"`

	// EnforceReturningUsageData controls whether to ask streaming providers to return usage data when possible.
	// When true, stream_options: {"include_usage": true} is added for provider paths that support it.
	// Default: true
	EnforceReturningUsageData bool `yaml:"enforce_returning_usage_data" env:"ENFORCE_RETURNING_USAGE_DATA"`

	// PricingRecalculationEnabled controls whether the admin pricing recalculation action is available.
	// Storage and pricing metadata support are still required; false always disables the feature.
	// Default: true
	PricingRecalculationEnabled bool `yaml:"pricing_recalculation_enabled" env:"USAGE_PRICING_RECALCULATION_ENABLED"`

	// ResponseCostEnabled returns the cost GoModel calculated for a request to
	// its caller, in OpenRouter's shape: usage.cost in the response's usage
	// objects, buffered and streamed, and the X-GoModel-Cost headers on
	// buffered responses. It is the billed cost when
	// pricing.markups_enabled is on, the provider cost otherwise. Turn it on
	// when clients account for their own spend per request.
	// Default: false
	ResponseCostEnabled bool `yaml:"response_cost_enabled" env:"USAGE_RESPONSE_COST_ENABLED"`

	// BufferSize is the number of usage entries to buffer before flushing
	// Default: 1000
	BufferSize int `yaml:"buffer_size" env:"USAGE_BUFFER_SIZE"`

	// FlushInterval is how often to flush buffered usage entries (in seconds)
	// Default: 5
	FlushInterval int `yaml:"flush_interval" env:"USAGE_FLUSH_INTERVAL"`

	// RetentionDays is how long to keep usage data (0 = forever)
	// Default: 90
	RetentionDays int `yaml:"retention_days" env:"USAGE_RETENTION_DAYS"`
}
