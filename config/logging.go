package config

import (
	"fmt"
	"net"
	"strings"
)

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
	// (for example GoModel Pro token compression) and every prompt guardrail
	// that edited the prompt store the request as they left it alongside the
	// original in the audit entry. Requires LogBodies.
	// Disabling it keeps the revision metadata (rewriter name, sizes, tokens
	// saved, change detail) but drops the rewritten body copy — roughly
	// halving audit storage per compressed request.
	// Default: true
	LogRevisionBodies bool `yaml:"log_revision_bodies" env:"LOGGING_LOG_REVISION_BODIES"`

	// LogGuardrailSteps records every prompt guardrail that edited the
	// request as its own revision in the audit entry, carrying the request
	// as that step left it, so a chain of edits reads step by step. Each
	// step leaves a copy of the prompt behind; building and encoding the
	// requests from those copies runs off the request path. Disabling it
	// records the
	// chain's edits as one revision (the request as forwarded) and skips
	// the per-step snapshots.
	// Default: true
	LogGuardrailSteps bool `yaml:"log_guardrail_steps" env:"LOGGING_LOG_GUARDRAIL_STEPS"`

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

	// TrustedProxyCIDRs lists the networks your own proxies sit on, enabling
	// X-Forwarded-For based client IPs in audit entries. A bare address is
	// treated as a single host.
	//
	// When empty (default), audit entries record the address of the socket
	// peer that connected to the gateway, and forwarding headers are ignored.
	// When set, an entry records the nearest hop in the X-Forwarded-For chain
	// that is not one of these networks (the last address your own proxy
	// wrote, which a client cannot overwrite). Requests that do not arrive from
	// a listed network keep their socket peer address.
	//
	// Loopback and private ranges are not trusted implicitly: list every proxy
	// hop between clients and the gateway, or the header chain is ignored.
	// Example: ["10.0.0.0/8", "127.0.0.1"].
	// Default: empty (forwarding headers ignored)
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs" env:"LOGGING_TRUSTED_PROXY_CIDRS"`
}

// NormalizeTrustedProxyCIDRs trims the configured networks, accepts bare
// addresses as single-host CIDRs, drops duplicates and blanks, and rejects
// anything that is neither a valid address nor a valid network.
func NormalizeTrustedProxyCIDRs(cfg *LogConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.TrustedProxyCIDRs) == 0 {
		cfg.TrustedProxyCIDRs = nil
		return nil
	}

	normalized := make([]string, 0, len(cfg.TrustedProxyCIDRs))
	seen := make(map[string]struct{}, len(cfg.TrustedProxyCIDRs))
	for _, raw := range cfg.TrustedProxyCIDRs {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		ip, ipnet, err := net.ParseCIDR(value)
		if err == nil {
			value, err = canonicalProxyCIDR(ip, ipnet)
			if err != nil {
				return err
			}
		} else if addr := net.ParseIP(value); addr != nil {
			value = singleHostCIDR(addr)
		} else {
			return fmt.Errorf("logging.trusted_proxy_cidrs: %q is not a valid IP address or CIDR network", raw)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	cfg.TrustedProxyCIDRs = normalized
	return nil
}

// canonicalProxyCIDR renders a parsed network in the form that actually matches
// requests, rejecting the ones that cannot. An IPv4-mapped network whose prefix
// is shorter than /96 ("::ffff:10.0.0.0/8") reaches past the embedded address:
// Go masks it to a network such as "::/8" that no IPv4 request belongs to, so
// the operator would believe a proxy network was trusted while nothing matched
// it. Narrower mapped networks ("::ffff:10.0.0.0/120") are rendered by net.IPNet
// as the IPv4 network they stand for ("10.0.0.0/24") and are kept.
func canonicalProxyCIDR(addr net.IP, ipnet *net.IPNet) (string, error) {
	ones, bits := ipnet.Mask.Size()
	if bits == 128 && addr.To4() != nil && ones < 96 {
		return "", fmt.Errorf("logging.trusted_proxy_cidrs: %q is an IPv4-mapped network with a prefix shorter than /96, which matches no IPv4 address; write the IPv4 form (for example 10.0.0.0/8)", ipnet.String())
	}
	return ipnet.String(), nil
}

// singleHostCIDR renders an address as the network containing only it, so an
// IPv4-mapped address such as "::ffff:10.0.0.1" normalizes to "10.0.0.1/32"
// instead of a /128 no IPv4 request will ever match.
func singleHostCIDR(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String() + "/32"
	}
	return ip.String() + "/128"
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
