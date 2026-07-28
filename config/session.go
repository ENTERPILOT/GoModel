package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// SessionConfig controls session keeping: identifying which requests belong to
// one client session for sticky load balancing and audit-log grouping.
type SessionConfig struct {
	// Enabled turns session identification on. Default: true.
	Enabled bool `yaml:"enabled" env:"SESSION_KEEPING_ENABLED"`

	// AutoDetect derives a session id from the conversation prefix of chat and
	// responses requests when no explicit signal is present. Default: true.
	AutoDetect bool `yaml:"auto_detect" env:"SESSION_AUTO_DETECT"`

	// BuiltinRules enables the built-in registry of session headers and body
	// fields known coding tools send. Default: true.
	BuiltinRules bool `yaml:"builtin_rules" env:"SESSION_BUILTIN_RULES"`

	// Headers declares additional session id headers, merged over the built-in
	// registry (an entry with a built-in header name overrides it).
	Headers []SessionHeaderConfig `yaml:"headers,omitempty"`
}

// SessionHeaderConfig declares one header to read session ids from.
type SessionHeaderConfig struct {
	// Header is the HTTP header name to read the session id from.
	Header string `yaml:"header" json:"header"`

	// Transform optionally post-processes the header value. Supported:
	// "session-uuid" (extract a session UUID from Anthropic metadata-style
	// values). Default: use the value as-is.
	Transform string `yaml:"transform,omitempty" json:"transform,omitempty"`
}

var sessionHeaderEnvRegex = regexp.MustCompile(`^SESSION_HEADER_([0-9]+)=`)

// applySessionEnv reads SESSION_HEADER_<N> env vars (with optional
// SESSION_HEADER_<N>_TRANSFORM companions) and merges them over the
// YAML-declared list, mirroring the tagging header env pipeline.
func applySessionEnv(cfg *Config) error {
	indexes := make([]int, 0)
	for _, kv := range os.Environ() {
		m := sessionHeaderEnvRegex.FindStringSubmatch(kv)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		indexes = append(indexes, n)
	}
	sort.Ints(indexes)

	fromEnv := make([]SessionHeaderConfig, 0, len(indexes))
	for _, n := range indexes {
		key := fmt.Sprintf("SESSION_HEADER_%d", n)
		header := strings.TrimSpace(os.Getenv(key))
		if header == "" {
			continue
		}
		fromEnv = append(fromEnv, SessionHeaderConfig{
			Header:    header,
			Transform: strings.TrimSpace(os.Getenv(key + "_TRANSFORM")),
		})
	}

	cfg.Session.Headers = mergeByKey(cfg.Session.Headers, fromEnv, func(header SessionHeaderConfig) string {
		return canonicalTextKey(header.Header)
	})
	return nil
}

// sessionTransforms are the transform names normalizeSessionConfig accepts.
// Kept in sync with the transforms internal/session implements.
var sessionTransforms = map[string]struct{}{
	"":             {},
	"session-uuid": {},
}

// normalizeSessionConfig canonicalizes header names and rejects invalid,
// credential-bearing, or duplicate entries.
func normalizeSessionConfig(cfg *SessionConfig) error {
	seen := make(map[string]struct{}, len(cfg.Headers))
	for i := range cfg.Headers {
		h := &cfg.Headers[i]
		name, err := NormalizeHeaderName(h.Header, "")
		if err != nil {
			return fmt.Errorf("session.headers[%d]: %w", i, err)
		}
		// Credential-bearing headers must never become session ids: the id is
		// persisted on audit and usage records in plaintext.
		if core.IsCredentialHeader(name) {
			return fmt.Errorf("session.headers[%d]: header %q may carry credentials and cannot be used for session ids", i, name)
		}
		h.Header = name
		if _, dup := seen[name]; dup {
			return fmt.Errorf("session.headers: duplicate header %q", name)
		}
		seen[name] = struct{}{}
		h.Transform = strings.ToLower(strings.TrimSpace(h.Transform))
		if _, ok := sessionTransforms[h.Transform]; !ok {
			return fmt.Errorf("session.headers[%d]: unknown transform %q (use \"session-uuid\" or omit)", i, h.Transform)
		}
	}
	return nil
}
