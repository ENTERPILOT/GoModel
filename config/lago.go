package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// Defaults for LagoConfig.
const (
	DefaultLagoAPIURL     = "https://api.getlago.com/api/v1"
	DefaultLagoBufferSize = 10000
)

// LagoConfig sends billable token usage to Lago (https://getlago.com) as
// usage events, so customers can be invoiced from there.
type LagoConfig struct {
	// Enabled turns the export on. It needs usage tracking (usage.enabled).
	// Default: false
	Enabled bool `yaml:"enabled" env:"LAGO_ENABLED"`

	// APIURL is the Lago API base URL. Change it for Lago's EU cluster
	// (https://api.eu.getlago.com/api/v1) or a self-hosted instance
	// (https://<host>/api/v1).
	// Default: "https://api.getlago.com/api/v1"
	APIURL string `yaml:"api_url" env:"LAGO_API_URL" export:"url"`

	// APIKey is the Lago API key, sent as a bearer token. Required when
	// Enabled.
	APIKey string `yaml:"api_key" env:"LAGO_API_KEY" export:"secret"`

	// Subscriptions maps a user path to the external_subscription_id of the
	// Lago subscription billed for it. The deepest path covering a request's
	// user path wins ("/acme" covers "/acme/team", not "/acmecorp"); a
	// request no path covers is not sent to Lago. LAGO_SUBSCRIPTIONS adds
	// entries as a comma-separated "user_path=subscription_id" list.
	// Default: none.
	Subscriptions map[string]string `yaml:"subscriptions"`

	// BufferSize is how many requests may wait to be sent. Raise it when
	// gomodel_lago_events_dropped_total{reason="buffer_full"} grows, which
	// happens when Lago is slow or unreachable for a while.
	// Default: 10000
	BufferSize int `yaml:"buffer_size" env:"LAGO_BUFFER_SIZE"`
}

// applyLagoEnv merges LAGO_SUBSCRIPTIONS into lago.subscriptions; an entry
// replaces the config.yaml entry of the same user path. The generic env
// overlay skips maps, so the variable is applied explicitly.
func applyLagoEnv(cfg *Config) error {
	raw := strings.TrimSpace(os.Getenv("LAGO_SUBSCRIPTIONS"))
	if raw == "" {
		return nil
	}
	merged := make(map[string]string, len(cfg.Lago.Subscriptions))
	for userPath, id := range cfg.Lago.Subscriptions {
		if canonical, err := core.NormalizeUserPath(userPath); err == nil {
			userPath = canonical
		}
		merged[userPath] = id
	}
	for item := range strings.SplitSeq(raw, ",") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		userPath, id, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("invalid value for LAGO_SUBSCRIPTIONS: %q is not user_path=subscription_id", strings.TrimSpace(item))
		}
		userPath, err := core.NormalizeUserPath(userPath)
		if err != nil {
			return fmt.Errorf("invalid value for LAGO_SUBSCRIPTIONS: %w", err)
		}
		merged[userPath] = id
	}
	cfg.Lago.Subscriptions = merged
	return nil
}

// normalizeLagoConfig validates the Lago export settings and rewrites the
// subscription user paths to their canonical form. A disabled export is left
// alone, so settings kept for later never stop startup.
func normalizeLagoConfig(cfg *LagoConfig) error {
	if !cfg.Enabled {
		return nil
	}
	cfg.APIURL = strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultLagoAPIURL
	}
	// A secret reference is resolved later and cannot be parsed here.
	if !HasSecretReference(cfg.APIURL) {
		parsed, err := url.Parse(cfg.APIURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("lago.api_url (LAGO_API_URL) must be an http or https URL")
		}
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("lago.api_key (LAGO_API_KEY) is required when lago.enabled is true")
	}
	// Expansion leaves an unset ${NAME} in config.yaml verbatim; sent as the
	// key, it would only ever be refused.
	if HasUnresolvedPlaceholder(cfg.APIKey) {
		return fmt.Errorf("lago.api_key reads an environment variable that is not set")
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultLagoBufferSize
	}
	if len(cfg.Subscriptions) == 0 {
		return fmt.Errorf("lago.subscriptions (LAGO_SUBSCRIPTIONS) is empty: map at least one user path to a Lago subscription")
	}

	subscriptions := make(map[string]string, len(cfg.Subscriptions))
	for raw, id := range cfg.Subscriptions {
		userPath, err := core.NormalizeUserPath(raw)
		if err != nil {
			return fmt.Errorf("lago.subscriptions[%q]: %w", raw, err)
		}
		switch userPath {
		case "":
			return fmt.Errorf("lago.subscriptions: user path is required")
		case "/":
			return fmt.Errorf("lago.subscriptions[%q]: \"/\" would bill every request to one subscription; map each customer's user path instead", raw)
		}
		if _, dup := subscriptions[userPath]; dup {
			return fmt.Errorf("lago.subscriptions: user path %q is configured more than once", userPath)
		}
		if id = strings.TrimSpace(id); id == "" {
			return fmt.Errorf("lago.subscriptions[%q]: subscription id is required", raw)
		}
		subscriptions[userPath] = id
	}
	cfg.Subscriptions = subscriptions
	return nil
}
