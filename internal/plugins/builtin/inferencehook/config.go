package inferencehook

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Action values for the "action" config key: what happens on a deny verdict.
const (
	ActionBlock   = "block"
	ActionRespond = "respond"
	ActionWarn    = "warn"
)

// Defaults for optional config keys.
const (
	DefaultMessage    = "This request was blocked by policy"
	DefaultAuthHeader = "Authorization"
	// DefaultVerdictTimeoutMS is the protocol's default verdict timeout.
	DefaultVerdictTimeoutMS = 5000
	// MaxVerdictTimeoutMS is the request timeout of the host's HTTP client.
	MaxVerdictTimeoutMS = 60000
)

// secretPrefix marks a Standard Webhooks signing secret.
const secretPrefix = "whsec_"

// settings is the validated configuration.
type settings struct {
	endpointURL  string
	endpointHost string
	signingKey   []byte
	authHeader   string
	authValue    string
	tenantID     string
	roles        map[pluginapi.Role]bool
	enforcement  pluginapi.Enforcement
	timeout      time.Duration
}

func decodeConfig(raw json.RawMessage) (settings, error) {
	cfg, err := pluginapi.ParseConfig(Name, New().Manifest().ConfigSchema, raw)
	if err != nil {
		return settings{}, err
	}
	s := settings{
		endpointURL: strings.TrimSpace(cfg.String("endpoint_url", "")),
		authHeader:  strings.TrimSpace(cfg.String("auth_header", DefaultAuthHeader)),
		authValue:   strings.TrimSpace(cfg.String("auth_value", "")),
		tenantID:    strings.TrimSpace(cfg.String("tenant_id", "")),
		roles:       cfg.Roles("roles", pluginapi.RoleSystem, pluginapi.RoleDeveloper, pluginapi.RoleUser, pluginapi.RoleAssistant, pluginapi.RoleTool),
		enforcement: pluginapi.Enforcement{
			Action:      pluginapi.Action(cfg.Choice("action", ActionBlock, ActionBlock, ActionRespond, ActionWarn)),
			Message:     cfg.String("message", DefaultMessage),
			BlockStatus: cfg.BlockStatus("block_status"),
		},
		timeout: time.Duration(cfg.Int("verdict_timeout_ms", DefaultVerdictTimeoutMS, 1, MaxVerdictTimeoutMS)) * time.Millisecond,
	}
	secret := strings.TrimSpace(cfg.String("signing_secret", ""))
	if err := cfg.Err(); err != nil {
		return settings{}, err
	}
	if len(s.roles) == 0 {
		return settings{}, fmt.Errorf("%s: roles must select at least one role, or the prompt is never inspected", Name)
	}
	if s.endpointHost, err = endpointHost(s.endpointURL); err != nil {
		return settings{}, err
	}
	if s.signingKey, err = signingKey(secret); err != nil {
		return settings{}, err
	}
	if s.authHeader == "" {
		s.authHeader = DefaultAuthHeader
	}
	if s.authValue != "" {
		if err := validateAuth(s); err != nil {
			return settings{}, err
		}
	}
	if strings.TrimSpace(s.enforcement.Message) == "" {
		s.enforcement.Message = DefaultMessage
	}
	return s, nil
}

// endpointHost validates the endpoint URL and returns its host for the
// dashboard summary. Credentials in the URL are refused: they would end up
// in logs, and the auth header is the place for them.
func endpointHost(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%s: endpoint_url is required", Name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s: endpoint_url must be an http:// or https:// URL", Name)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s: endpoint_url must not carry credentials; use auth_header and auth_value", Name)
	}
	return u.Host, nil
}

// signingKey decodes a Standard Webhooks secret: standard base64, with or
// without the whsec_ prefix.
func signingKey(secret string) ([]byte, error) {
	if secret == "" {
		return nil, fmt.Errorf("%s: signing_secret is required", Name)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, secretPrefix))
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("%s: signing_secret must be standard base64, with or without the %s prefix", Name, secretPrefix)
	}
	return key, nil
}

// reservedHeaders are set by the plugin on every request.
var reservedHeaders = map[string]bool{
	"Content-Type":      true,
	"Content-Length":    true,
	"Accept-Encoding":   true,
	"Host":              true,
	"User-Agent":        true,
	"Webhook-Id":        true,
	"Webhook-Timestamp": true,
	"Webhook-Signature": true,
}

func validateAuth(s settings) error {
	if !validHeaderName(s.authHeader) || reservedHeaders[http.CanonicalHeaderKey(s.authHeader)] {
		return fmt.Errorf("%s: auth_header %q is not a header name the plugin can send", Name, s.authHeader)
	}
	if strings.ContainsAny(s.authValue, "\r\n\x00") {
		return fmt.Errorf("%s: auth_value must be a single line", Name)
	}
	if !strings.HasPrefix(s.endpointURL, "https://") && !loopbackHost(s.endpointHost) {
		return fmt.Errorf("%s: auth_value needs an https:// endpoint_url (plain http is only allowed for localhost), so the credential is not sent in clear", Name)
	}
	return nil
}

// validHeaderName reports whether name is an HTTP token of letters, digits,
// and hyphens, the characters every server and proxy accepts.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// loopbackHost reports whether a URL host (with or without a port) is this
// machine.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
