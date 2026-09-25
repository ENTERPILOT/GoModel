package config

import (
	"fmt"
	"net/textproto"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/platformdir"
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
	Port      string `yaml:"port" env:"PORT"`
	BasePath  string `yaml:"base_path" env:"BASE_PATH"`           // URL path prefix where the app is mounted (e.g., "/g")
	MasterKey string `yaml:"master_key" env:"GOMODEL_MASTER_KEY"` // Optional: Master key for authentication
	// MasterKeyDisabled turns off master key authentication. Default: false.
	// Set it to leave managed keys and any configured authentication extension
	// as the only credentials: the master key is forgotten at startup, and the
	// gateway then requires a credential on every request instead of falling
	// back to unauthenticated access or opening the admin API for bootstrap.
	MasterKeyDisabled bool   `yaml:"master_key_disabled" env:"MASTER_KEY_DISABLED"`
	BodySizeLimit     string `yaml:"body_size_limit" env:"BODY_SIZE_LIMIT"` // Max request body size (e.g., "10M", "1024K")
	SwaggerEnabled    bool   `yaml:"swagger_enabled" env:"SWAGGER_ENABLED"` // Whether to expose the Swagger UI at /swagger/index.html
	PprofEnabled      bool   `yaml:"pprof_enabled" env:"PPROF_ENABLED"`     // Whether to expose debug profiling routes at /debug/pprof/*
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
	// ["openai", "anthropic", "openrouter", "kilo", "zai", "sglang", "vllm", "llamacpp", "llmd", "deepseek", "jev"].
	EnabledPassthroughProviders []string `yaml:"enabled_passthrough_providers" env:"ENABLED_PASSTHROUGH_PROVIDERS"`
	// RealtimeEnabled exposes the realtime (speech-to-speech) websocket endpoints
	// at /v1/realtime and /v1/realtime/translations, their WebRTC signaling
	// siblings, and the /p/{provider}/v1/realtime passthrough upgrade.
	// Default: true. Only providers implementing realtime accept sessions.
	RealtimeEnabled bool `yaml:"realtime_enabled" env:"REALTIME_ENABLED"`
	// AuthVerifyEnabled exposes GET /v1/auth/verify, which reports whether the
	// API key a request carries authenticates against this gateway. It sits
	// outside /admin so it keeps working when the admin API is disabled.
	// Default: false — turn it on only when a service in front of the gateway
	// needs to validate keys without holding a copy of them.
	AuthVerifyEnabled bool `yaml:"auth_verify_enabled" env:"AUTH_VERIFY_ENABLED"`
	// PIDFile records the process id of the running gateway so `gomodel --reload`
	// can find it. Default: DefaultPIDFilePath(). Set it per instance when
	// several gateways share a host, or to "" in config.yaml to write no pid
	// file at all, which also disables `--reload` (an empty PID_FILE reads as
	// unset, like every other env var here, and keeps the default). Changing it
	// needs a restart — it names the process that is already running — so a
	// reload only warns about it.
	PIDFile string `yaml:"pid_file" env:"PID_FILE"`
	// StreamStallTimeout bounds, in seconds, how long a single response write
	// on a model interaction route may wait for the client to accept bytes
	// before the connection is dropped. It fires only when the client has
	// stopped reading (its socket buffer is full), never while the gateway is
	// waiting on the provider, so slow models are unaffected. Without it a
	// client that stops reading a stream pins a goroutine and the upstream
	// provider connection until the provider side times out.
	// Default: 60 (DefaultStreamStallTimeoutSeconds). 0 disables the limit.
	StreamStallTimeout int `yaml:"stream_stall_timeout" env:"STREAM_STALL_TIMEOUT"`

	// TrustedProxies lists the networks your own proxies sit on, enabling
	// header-based client addresses everywhere the gateway reports one (audit
	// entries, rate limit keys, logs). Entries are CIDR networks, bare
	// addresses (a single host), or the presets "loopback" and "private".
	//
	// When empty (default), the gateway records the address of the socket peer
	// that connected to it and ignores every forwarding header. Nothing is
	// trusted implicitly, not even loopback: list the hops between your
	// clients and the gateway, or name the preset that covers them.
	// Example: ["10.42.0.0/16", "loopback"].
	// Default: empty (forwarding headers ignored)
	TrustedProxies []string `yaml:"trusted_proxies" env:"SERVER_TRUSTED_PROXIES"`

	// ClientIPHeader is the header a trusted proxy passes the client address
	// in. X-Forwarded-For (the default) carries a chain and is resolved hop by
	// hop; any other header (X-Real-IP, CF-Connecting-IP, True-Client-IP)
	// carries one address and is taken verbatim, which is the correct reading
	// behind an edge like Cloudflare that writes the client itself. Requires
	// TrustedProxies. RFC 7239 Forwarded is not supported.
	// Default: X-Forwarded-For
	ClientIPHeader string `yaml:"client_ip_header" env:"SERVER_CLIENT_IP_HEADER"`

	// TrustedHops is how many proxies sit between the client and the gateway,
	// selecting the X-Forwarded-For entry at that depth instead of scanning the
	// chain against TrustedProxies. Use it when your edge is at a fixed depth
	// but rotates through addresses you cannot enumerate; the socket peer must
	// still be in TrustedProxies. A chain shorter than this depth falls back to
	// the socket peer. Requires TrustedProxies and the default header.
	// Default: 0 (scan the chain against TrustedProxies)
	TrustedHops int `yaml:"trusted_hops" env:"SERVER_TRUSTED_HOPS"`

	// ClientIP is the compiled form of the three settings above, resolved once
	// at load. It is not configured directly.
	ClientIP ClientIPPolicy `yaml:"-"`
}

// DefaultStreamStallTimeoutSeconds is the default ServerConfig.StreamStallTimeout.
// It matches the send timeout most reverse proxies apply between two
// successive writes to a client.
const DefaultStreamStallTimeoutSeconds = 60

// LegacyPIDFilePath is the pid file location used next to a project-local
// ./data directory, matching where the SQLite database lands in the same setup.
const LegacyPIDFilePath = "data/gomodel.pid"

// DefaultPIDFilePath returns the pid file path used when none is configured:
// LegacyPIDFilePath when a ./data directory already exists (Docker images and
// existing deployments), otherwise the OS-conventional per-user data directory
// — the same resolution the database uses, so both land together.
func DefaultPIDFilePath() string {
	return platformdir.DataFile("gomodel.pid")
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
