package config

import "strings"

// LogConfig holds audit logging configuration
type LogConfig struct {
	// Enabled controls whether audit logging is active
	// Default: false
	Enabled bool `yaml:"enabled" env:"LOGGING_ENABLED"`

	// LogBodies enables logging of full request/response bodies
	// WARNING: May contain sensitive data (PII, API keys in prompts)
	// Default: true
	LogBodies bool `yaml:"log_bodies" env:"LOGGING_LOG_BODIES"`

	// LogAudioBodies refines LogBodies for audio endpoints: when both are
	// enabled, the /v1/audio/speech JSON input and binary audio output are
	// stored (audio as base64 for playback) and /v1/audio/transcriptions upload
	// metadata is recorded. Requires LogBodies (the master body-logging switch);
	// when LogBodies is on but this is off, audio responses are recorded as a
	// lightweight placeholder instead of the full bytes.
	// WARNING: stores full audio in the audit log; grows storage quickly.
	// Default: false
	LogAudioBodies bool `yaml:"log_audio_bodies" env:"LOGGING_LOG_AUDIO_BODIES"`

	// LogImageBodies refines LogBodies for the image endpoints
	// (/v1/images/generations, /v1/images/edits): when both are enabled the
	// image bytes (uploaded sources and masks, generated outputs) are stored as
	// base64 so the dashboard can display them. Requires LogBodies; when
	// LogBodies is on but this is off, image bodies keep their metadata
	// (prompt, parameters, sizes, usage) and URLs but drop the pixels.
	// WARNING: stores full images in the audit log; grows storage quickly.
	// Default: false
	LogImageBodies bool `yaml:"log_image_bodies" env:"LOGGING_LOG_IMAGE_BODIES"`

	// LogImageBodiesScope narrows LogImageBodies to one direction: "all"
	// stores uploaded inputs and generated outputs, "input" only the uploads
	// (edit sources and masks), "output" only the generated images. Ignored
	// while LogImageBodies is off.
	// Default: all
	LogImageBodiesScope ImageBodyScope `yaml:"log_image_bodies_scope" env:"LOGGING_LOG_IMAGE_BODIES_SCOPE"`

	// LogRevisionBodies refines LogBodies for the request-revision chain:
	// when both are enabled, every request rewriter that changed the body
	// (for example GoModel Pro token compression) stores the full rewritten
	// body alongside the original in the audit entry. Requires LogBodies.
	// Disabling it keeps the revision metadata (rewriter name, sizes, tokens
	// saved, change detail) but drops the rewritten body copy — roughly
	// halving audit storage per compressed request.
	// Default: true
	LogRevisionBodies bool `yaml:"log_revision_bodies" env:"LOGGING_LOG_REVISION_BODIES"`

	// LogHeaders enables logging of request/response headers
	// Sensitive headers (Authorization, Cookie, etc.) are auto-redacted
	// Default: true
	LogHeaders bool `yaml:"log_headers" env:"LOGGING_LOG_HEADERS"`

	// BufferSize is the number of log entries to buffer before flushing
	// Default: 1000
	BufferSize int `yaml:"buffer_size" env:"LOGGING_BUFFER_SIZE"`

	// FlushInterval is how often to flush buffered logs (in seconds)
	// Default: 5
	FlushInterval int `yaml:"flush_interval" env:"LOGGING_FLUSH_INTERVAL"`

	// RetentionDays is how long to keep logs (0 = forever)
	// Default: 30
	RetentionDays int `yaml:"retention_days" env:"LOGGING_RETENTION_DAYS"`

	// OnlyModelInteractions limits audit logging to AI model endpoints only
	// When true, only /v1/chat/completions, /v1/responses, /v1/embeddings, /v1/files, and /v1/batches are logged
	// Endpoints like /health, /metrics, /admin, /v1/models are skipped
	// Default: true
	OnlyModelInteractions bool `yaml:"only_model_interactions" env:"LOGGING_ONLY_MODEL_INTERACTIONS"`
}

// ImageBodyScope selects which image bytes the audit log embeds when
// LogImageBodies is enabled.
type ImageBodyScope string

const (
	ImageBodyScopeAll    ImageBodyScope = "all"
	ImageBodyScopeInput  ImageBodyScope = "input"
	ImageBodyScopeOutput ImageBodyScope = "output"
)

// ResolveImageBodyScope normalizes a configured scope, defaulting to all.
func ResolveImageBodyScope(value ImageBodyScope) ImageBodyScope {
	normalized := ImageBodyScope(strings.ToLower(strings.TrimSpace(string(value))))
	if normalized == "" {
		return ImageBodyScopeAll
	}
	return normalized
}

// Valid reports whether the scope is one of the supported values.
func (s ImageBodyScope) Valid() bool {
	switch s {
	case ImageBodyScopeAll, ImageBodyScopeInput, ImageBodyScopeOutput:
		return true
	default:
		return false
	}
}

// Inputs reports whether uploaded images (edit sources and masks) are stored.
func (s ImageBodyScope) Inputs() bool {
	return s == ImageBodyScopeAll || s == ImageBodyScopeInput
}

// Outputs reports whether generated images are stored.
func (s ImageBodyScope) Outputs() bool {
	return s == ImageBodyScopeAll || s == ImageBodyScopeOutput
}
