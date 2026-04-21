// Package auditlog provides audit logging for the AI gateway.
// It captures request/response metadata and stores it in configurable backends.
package auditlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// LogStore defines the interface for audit log storage backends.
// Implementations must be safe for concurrent use.
type LogStore interface {
	// WriteBatch writes multiple log entries to storage.
	// This is called by the Logger when flushing buffered entries.
	WriteBatch(ctx context.Context, entries []*LogEntry) error

	// Flush forces any pending writes to complete.
	// Called during graceful shutdown.
	Flush(ctx context.Context) error

	// Close releases resources and flushes pending writes.
	Close() error
}

const (
	CacheTypeExact    = "exact"
	CacheTypeSemantic = "semantic"

	AuthMethodAPIKey    = "api_key"
	AuthMethodMasterKey = "master_key"
	AuthMethodNoKey     = "no_key"
)

// LogEntry represents a single audit log entry.
// Core fields are indexed for efficient queries.
type LogEntry struct {
	// ID is a unique identifier for this log entry (UUID)
	ID string `json:"id" bson:"_id"`

	// Timestamp is when the request started
	Timestamp time.Time `json:"timestamp" bson:"timestamp"`

	// DurationNs is the request duration in nanoseconds
	DurationNs int64 `json:"duration_ns" bson:"duration_ns"`

	// Core fields (indexed for queries)
	RequestedModel    string `json:"requested_model" bson:"requested_model,omitempty"`
	ResolvedModel     string `json:"resolved_model,omitempty" bson:"resolved_model,omitempty"`
	Provider          string `json:"provider" bson:"provider"` // canonical provider type used for routing and filters
	ProviderName      string `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	AliasUsed         bool   `json:"alias_used,omitempty" bson:"alias_used,omitempty"`
	WorkflowVersionID string `json:"workflow_version_id,omitempty" bson:"workflow_version_id,omitempty"`
	CacheType         string `json:"cache_type,omitempty" bson:"cache_type,omitempty"`
	StatusCode        int    `json:"status_code" bson:"status_code"`

	// Extracted fields for efficient filtering (indexed in relational DBs)
	RequestID  string `json:"request_id,omitempty" bson:"request_id,omitempty"`
	AuthKeyID  string `json:"auth_key_id,omitempty" bson:"auth_key_id,omitempty"`
	AuthMethod string `json:"auth_method,omitempty" bson:"auth_method,omitempty"`
	ClientIP   string `json:"client_ip,omitempty" bson:"client_ip,omitempty"`
	Method     string `json:"method,omitempty" bson:"method,omitempty"`
	Path       string `json:"path,omitempty" bson:"path,omitempty"`
	UserPath   string `json:"user_path,omitempty" bson:"user_path,omitempty"`
	Stream     bool   `json:"stream,omitempty" bson:"stream,omitempty"`
	ErrorType  string `json:"error_type,omitempty" bson:"error_type,omitempty"`

	// Data contains flexible request/response information as JSON
	Data *LogData `json:"data,omitempty" bson:"data,omitempty"`
}

// LogData contains flexible request/response information.
// Fields that are commonly filtered are stored as columns in LogEntry.
// This struct contains the remaining flexible data.
type LogData struct {
	// Identity
	UserAgent  string `json:"user_agent,omitempty" bson:"user_agent,omitempty"`
	APIKeyHash string `json:"api_key_hash,omitempty" bson:"api_key_hash,omitempty"`

	// WorkflowFeatures captures the request-time effective workflow features
	// after runtime caps were applied. This keeps audit views historically accurate
	// even if the active process config changes later.
	WorkflowFeatures *WorkflowFeaturesSnapshot `json:"workflow_features,omitempty" bson:"workflow_features,omitempty"`

	// Failover captures runtime redirect details when translated execution
	// moved from the primary selector to a configured failover target.
	Failover *FailoverSnapshot `json:"failover,omitempty" bson:"failover,omitempty"`

	PassthroughEndpoint string `json:"passthrough_endpoint,omitempty" bson:"passthrough_endpoint,omitempty"`

	// Request parameters
	Temperature *float64 `json:"temperature,omitempty" bson:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty" bson:"max_tokens,omitempty"`

	// Error details (message can be long, so kept in JSON)
	ErrorMessage string `json:"error_message,omitempty" bson:"error_message,omitempty"`

	// Optional headers (when LOGGING_LOG_HEADERS=true)
	// Sensitive headers are auto-redacted
	RequestHeaders  map[string]string `json:"request_headers,omitempty" bson:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty" bson:"response_headers,omitempty"`

	// Optional bodies (when LOGGING_LOG_BODIES=true)
	// Stored as interface{} so MongoDB serializes as native BSON documents (queryable/readable)
	// instead of BSON Binary (base64 in Compass)
	RequestBody  any `json:"request_body,omitempty" bson:"request_body,omitempty"`
	ResponseBody any `json:"response_body,omitempty" bson:"response_body,omitempty"`

	// Body capture status flags (set when body exceeds 1MB limit)
	RequestBodyTooBigToHandle  bool `json:"request_body_too_big_to_handle,omitempty" bson:"request_body_too_big_to_handle,omitempty"`
	ResponseBodyTooBigToHandle bool `json:"response_body_too_big_to_handle,omitempty" bson:"response_body_too_big_to_handle,omitempty"`
}

// WorkflowFeaturesSnapshot stores the effective workflow feature state that
// applied to one request. Fields intentionally do not use omitempty so "false"
// remains explicit once the snapshot exists.
type WorkflowFeaturesSnapshot struct {
	Cache      bool `json:"cache" bson:"cache"`
	Audit      bool `json:"audit" bson:"audit"`
	Usage      bool `json:"usage" bson:"usage"`
	Guardrails bool `json:"guardrails" bson:"guardrails"`
	Fallback   bool `json:"fallback" bson:"fallback"`
}

// FailoverSnapshot stores the runtime failover selection used for one request.
// The target model is the configured failover selector, not the model echoed by
// the provider response body.
type FailoverSnapshot struct {
	TargetModel string `json:"target_model,omitempty" bson:"target_model,omitempty"`
}

// marshalLogData marshals the Data field to JSON for SQL storage.
// Returns nil if data is nil, or "{}" if marshaling fails.
// This is used by PostgreSQL and SQLite stores.
func marshalLogData(data *LogData, entryID string) []byte {
	if data == nil {
		return nil
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal log data", "error", err, "id", entryID)
		return []byte("{}")
	}
	return dataJSON
}

func normalizeCacheType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CacheTypeExact:
		return CacheTypeExact
	case CacheTypeSemantic:
		return CacheTypeSemantic
	default:
		return ""
	}
}

func displayAuditProviderName(providerName, provider string) string {
	if trimmed := strings.TrimSpace(providerName); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(provider)
}

// RedactedHeaders contains headers that should be automatically redacted.
// Values are replaced with "[REDACTED]" to prevent leaking secrets.
var RedactedHeaders = []string{
	"authorization",
	"x-api-key",
	"cookie",
	"set-cookie",
	"x-auth-token",
	"x-access-token",
	"proxy-authorization",
	"x-gomodel-key",
}

// redactedHeadersSet is built once at package init for O(1) lookups.
var redactedHeadersSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(RedactedHeaders))
	for _, h := range RedactedHeaders {
		set[h] = struct{}{}
	}
	return set
}()

// RedactHeaders redacts sensitive headers from a header map.
// The original map is not modified; a new map is returned.
func RedactHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}

	result := make(map[string]string, len(headers))
	for key, value := range headers {
		if _, ok := redactedHeadersSet[strings.ToLower(key)]; ok {
			result[key] = "[REDACTED]"
		} else {
			result[key] = value
		}
	}
	return result
}

// Config holds audit logging configuration
type Config struct {
	// Enabled controls whether audit logging is active
	Enabled bool

	// LogBodies enables logging of full request/response bodies
	LogBodies bool

	// LogHeaders enables logging of request/response headers
	LogHeaders bool

	// BufferSize is the number of log entries to buffer before flushing
	BufferSize int

	// FlushInterval is how often to flush buffered logs
	FlushInterval time.Duration

	// RetentionDays is how long to keep logs (0 = forever)
	RetentionDays int

	// OnlyModelInteractions limits logging to AI model endpoints only
	// When true, only /v1/chat/completions, /v1/responses, /v1/embeddings, /v1/files, and /v1/batches are logged
	OnlyModelInteractions bool
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Enabled:               false,
		LogBodies:             false,
		LogHeaders:            false,
		BufferSize:            1000,
		FlushInterval:         5 * time.Second,
		RetentionDays:         30,
		OnlyModelInteractions: true,
	}
}
