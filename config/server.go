package config

import (
	"fmt"
	"net/textproto"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Body size limit constants
const (
	DefaultBodySizeLimit int64 = 10 * 1024 * 1024  // 10MB
	MinBodySizeLimit     int64 = 1 * 1024          // 1KB
	MaxBodySizeLimit     int64 = 100 * 1024 * 1024 // 100MB
)

var bodySizeLimitRegex = regexp.MustCompile(`(?i)^(\d+)([KMG])?B?$`)

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port           string `yaml:"port" env:"PORT"`
	BasePath       string `yaml:"base_path" env:"BASE_PATH"`             // URL path prefix where the app is mounted (e.g., "/g")
	MasterKey      string `yaml:"master_key" env:"GOMODEL_MASTER_KEY"`   // Optional: Master key for authentication
	BodySizeLimit  string `yaml:"body_size_limit" env:"BODY_SIZE_LIMIT"` // Max request body size (e.g., "10M", "1024K")
	SwaggerEnabled bool   `yaml:"swagger_enabled" env:"SWAGGER_ENABLED"` // Whether to expose the Swagger UI at /swagger/index.html
	PprofEnabled   bool   `yaml:"pprof_enabled" env:"PPROF_ENABLED"`     // Whether to expose debug profiling routes at /debug/pprof/*
	// EnablePassthroughRoutes exposes provider-native passthrough endpoints under
	// /p/{provider}/{endpoint}. Default: true.
	EnablePassthroughRoutes bool `yaml:"enable_passthrough_routes" env:"ENABLE_PASSTHROUGH_ROUTES"`
	// AllowPassthroughV1Alias allows /p/{provider}/v1/... style passthrough routes
	// while keeping /p/{provider}/... as the canonical form. Default: true.
	AllowPassthroughV1Alias bool `yaml:"allow_passthrough_v1_alias" env:"ALLOW_PASSTHROUGH_V1_ALIAS"`
	// UserPathHeader is the inbound HTTP header used to read/write user paths.
	// Default: X-GoModel-User-Path.
	UserPathHeader string `yaml:"user_path_header" env:"USER_PATH_HEADER"`
	// EnabledPassthroughProviders lists the provider types enabled on
	// /p/{provider}/... passthrough routes. Default:
	// ["openai", "anthropic", "openrouter", "kilo", "zai", "vllm", "deepseek"].
	EnabledPassthroughProviders []string `yaml:"enabled_passthrough_providers" env:"ENABLED_PASSTHROUGH_PROVIDERS"`
	// RealtimeEnabled exposes the realtime (speech-to-speech) websocket endpoint
	// at /v1/realtime and the /p/{provider}/v1/realtime passthrough upgrade.
	// Default: true. Only providers implementing realtime accept sessions.
	RealtimeEnabled bool `yaml:"realtime_enabled" env:"REALTIME_ENABLED"`
}

var headerNameRegex = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

// NormalizeHeaderName canonicalizes an HTTP header field name. Empty values
// fall back to fallback.
func NormalizeHeaderName(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if !headerNameRegex.MatchString(value) {
		return "", fmt.Errorf("invalid HTTP header name %q", value)
	}
	if strings.EqualFold(value, fallback) {
		return fallback, nil
	}
	return textproto.CanonicalMIMEHeaderKey(value), nil
}

// NormalizeBasePath canonicalizes the public mount path for the HTTP server.
// Empty, whitespace-only, and "/" all resolve to root.
func NormalizeBasePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "/" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	normalized := path.Clean(trimmed)
	if normalized == "." || normalized == "/" {
		return "/"
	}
	return normalized
}

// JoinBasePath prefixes urlPath with the normalized public mount path.
func JoinBasePath(basePath, urlPath string) string {
	basePath = NormalizeBasePath(basePath)
	trimmedPath := strings.TrimSpace(urlPath)
	if trimmedPath == "" || trimmedPath == "/" {
		if basePath == "/" {
			return "/"
		}
		return basePath
	}
	if !strings.HasPrefix(trimmedPath, "/") {
		trimmedPath = "/" + trimmedPath
	}
	if basePath == "/" {
		return trimmedPath
	}
	return basePath + trimmedPath
}

// ValidateBodySizeLimit validates a body size limit string.
// Accepts formats like: "10M", "10MB", "1024K", "1024KB", "104857600"
// Returns an error if the format is invalid or value is outside bounds (1KB - 100MB).
func ValidateBodySizeLimit(s string) error {
	_, err := ParseBodySizeLimitBytes(s)
	return err
}

// ParseBodySizeLimitBytes parses a configured body size limit into bytes.
// Accepts formats like: "10M", "10MB", "1024K", "1024KB", "104857600".
// Returns an error if the format is invalid or value is outside bounds (1KB - 100MB).
func ParseBodySizeLimitBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	matches := bodySizeLimitRegex.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid format %q: expected pattern like '10M', '1024K', or '104857600'", s)
	}

	value, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in %q: %w", s, err)
	}

	switch strings.ToUpper(matches[2]) {
	case "K":
		value *= 1024
	case "M":
		value *= 1024 * 1024
	case "G":
		value *= 1024 * 1024 * 1024
	}

	if value < MinBodySizeLimit {
		return 0, fmt.Errorf("value %d bytes is below minimum of %d bytes (1KB)", value, MinBodySizeLimit)
	}
	if value > MaxBodySizeLimit {
		return 0, fmt.Errorf("value %d bytes exceeds maximum of %d bytes (100MB)", value, MaxBodySizeLimit)
	}

	return value, nil
}
