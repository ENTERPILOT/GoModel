// Package auditlog provides audit logging for the AI gateway.
// It captures request/response metadata and stores it in configurable backends.
package auditlog

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-json"
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

const (
	AttemptKindPrimary  = "primary"
	AttemptKindFailover = "failover"
	AttemptKindRetry    = "retry"
)

const (
	LiveEventAuditStarted   = "audit.started"
	LiveEventAuditUpdated   = "audit.updated"
	LiveEventAuditCompleted = "audit.completed"
	LiveEventAuditFailed    = "audit.failed"
	LiveEventAuditFlushed   = "audit.flushed"
	LiveEventAuditRemoved   = "audit.removed"
)

const maxAttemptErrorMessageLength = 2048

// LiveEventPublisher receives compact audit lifecycle snapshots for realtime
// dashboard preview. Implementations must not block request handling.
type LiveEventPublisher interface {
	PublishAuditEvent(eventType string, entry *LogEntry)
}

// LiveEventEmitter is implemented by loggers that can publish audit lifecycle
// previews before the entry is persisted.
type LiveEventEmitter interface {
	PublishLiveEvent(eventType string, entry *LogEntry)
}

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

	// Attempts captures provider calls made for this logical request. SQL
	// stores split this into audit_log_attempts; Mongo stores it embedded.
	Attempts []AttemptSnapshot `json:"attempts,omitempty" bson:"attempts,omitempty"`

	// Request parameters
	Temperature *float64 `json:"temperature,omitempty" bson:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty" bson:"max_tokens,omitempty"`

	// Error details (message can be long, so kept in JSON)
	ErrorMessage string `json:"error_message,omitempty" bson:"error_message,omitempty"`
	ErrorCode    string `json:"error_code,omitempty" bson:"error_code,omitempty"`

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
	Budget     bool `json:"budget" bson:"budget"`
	Guardrails bool `json:"guardrails" bson:"guardrails"`
	Fallback   bool `json:"fallback" bson:"fallback"`
}

// FailoverSnapshot stores the runtime failover selection used for one request.
// The target model is the configured failover selector, not the model echoed by
// the provider response body.
type FailoverSnapshot struct {
	TargetModel string `json:"target_model,omitempty" bson:"target_model,omitempty"`
}

// AttemptSnapshot stores one external provider attempt made for a logical
// request. It intentionally stores structured errors, not raw upstream bodies.
type AttemptSnapshot struct {
	Seq          int       `json:"seq" bson:"seq"`
	Kind         string    `json:"kind" bson:"kind"`
	ProviderType string    `json:"provider_type,omitempty" bson:"provider_type,omitempty"`
	ProviderName string    `json:"provider_name,omitempty" bson:"provider_name,omitempty"`
	Model        string    `json:"model,omitempty" bson:"model,omitempty"`
	StatusCode   int       `json:"status_code,omitempty" bson:"status_code,omitempty"`
	Success      bool      `json:"success" bson:"success"`
	ErrorType    string    `json:"error_type,omitempty" bson:"error_type,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty" bson:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty" bson:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty" bson:"started_at,omitempty"`
	DurationNs   int64     `json:"duration_ns,omitempty" bson:"duration_ns,omitempty"`

	// ResponseBody and ResponseHeaders capture the raw upstream error response
	// of a failed attempt. ResponseBody is the parsed JSON (or a string when the
	// body is not JSON); ResponseHeaders is redacted. Both are populated only
	// when audit body/header logging is enabled.
	ResponseBody    any               `json:"response_body,omitempty" bson:"response_body,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty" bson:"response_headers,omitempty"`
}

// marshalLogData marshals the Data field to JSON for SQL storage.
// Returns nil if data is nil, or "{}" if marshaling fails.
// This is used by PostgreSQL and SQLite stores.
func marshalLogData(data *LogData, entryID string) []byte {
	if data == nil {
		return nil
	}
	data = data.withoutAttempts()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal log data", "error", err, "id", entryID)
		return []byte("{}")
	}
	return dataJSON
}

func (d *LogData) withoutAttempts() *LogData {
	if d == nil || len(d.Attempts) == 0 {
		return d
	}
	clean := *d
	clean.Attempts = nil
	return &clean
}

func auditAttempts(entry *LogEntry) []AttemptSnapshot {
	if entry == nil || entry.Data == nil || len(entry.Data.Attempts) == 0 {
		return nil
	}
	return normalizeAttemptSnapshots(entry.Data.Attempts)
}

func normalizeAttemptSnapshots(attempts []AttemptSnapshot) []AttemptSnapshot {
	if len(attempts) == 0 {
		return nil
	}
	normalized := make([]AttemptSnapshot, 0, len(attempts))
	for _, attempt := range attempts {
		attempt.Kind = normalizeAttemptKind(attempt.Kind)
		if attempt.Kind == "" {
			continue
		}
		if attempt.Seq <= 0 {
			attempt.Seq = len(normalized) + 1
		}
		attempt.ProviderType = strings.TrimSpace(attempt.ProviderType)
		attempt.ProviderName = strings.TrimSpace(attempt.ProviderName)
		attempt.Model = strings.TrimSpace(attempt.Model)
		attempt.ErrorType = strings.TrimSpace(attempt.ErrorType)
		attempt.ErrorCode = strings.TrimSpace(attempt.ErrorCode)
		attempt.ErrorMessage = truncateAttemptErrorMessage(strings.TrimSpace(attempt.ErrorMessage))
		normalized = append(normalized, attempt)
	}
	return normalized
}

func normalizeAttemptKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case AttemptKindPrimary:
		return AttemptKindPrimary
	case AttemptKindFailover:
		return AttemptKindFailover
	case AttemptKindRetry:
		return AttemptKindRetry
	default:
		return ""
	}
}

func truncateAttemptErrorMessage(message string) string {
	if len(message) <= maxAttemptErrorMessageLength {
		return message
	}
	// Back the cut off the byte limit to the nearest rune boundary so a
	// multi-byte rune (common in non-ASCII provider errors) is never split into
	// invalid UTF-8, which would be mangled when JSON-marshaled for storage.
	cut := maxAttemptErrorMessageLength
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
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

	// LogAudioBodies refines LogBodies for audio endpoints (base64 audio for
	// /v1/audio/speech, upload metadata for transcriptions). Requires LogBodies:
	// when LogBodies is off no audio body is captured; when LogBodies is on but
	// this is off, audio responses are recorded as a lightweight placeholder.
	LogAudioBodies bool

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
		LogAudioBodies:        false,
		LogHeaders:            false,
		BufferSize:            1000,
		FlushInterval:         5 * time.Second,
		RetentionDays:         30,
		OnlyModelInteractions: true,
	}
}
