package config

import (
	"fmt"
	"os"
	"strings"
)

// UserConfig declares one user-path policy in config.yaml or the USERS env
// var, so model access and request policy can be managed as
// infrastructure-as-code. A declared policy shadows the admin-store row of
// the same path and is read-only in the dashboard.
type UserConfig struct {
	// Path is the user path (group or user) the policy applies to, e.g.
	// "/acme/eng". Deeper paths inherit it.
	Path string `yaml:"path" json:"path"`

	// AllowedModels lists the model selectors requests under Path may use:
	// exact "provider/model", provider-wide "provider/*", or model-wide
	// "model". Empty means the node itself does not restrict models.
	AllowedModels []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`

	// DefaultModel serves chat, Responses and Messages requests under Path
	// that name neither a model nor a provider: a model or a virtual model.
	// The nearest path that sets one wins. Empty (the default) keeps
	// rejecting such requests.
	DefaultModel string `yaml:"default_model,omitempty" json:"default_model,omitempty"`

	// MaxReasoningEffort caps the reasoning effort of requests under Path:
	// "none", "minimal", "low", "medium", "high", "xhigh", or "max". A
	// higher request is lowered to it; the lowest cap up to the root
	// applies. A model that cannot reason below a minimum level keeps that
	// minimum. Empty (the default) sets no cap.
	MaxReasoningEffort string `yaml:"max_reasoning_effort,omitempty" json:"max_reasoning_effort,omitempty"`

	// MaxOutputTokens caps the output token limit of requests under Path. A
	// higher request is lowered to it and a request without a limit gets it;
	// the lowest cap up to the root applies. Zero (the default) sets no cap.
	MaxOutputTokens int `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`

	// Description is an optional human-readable note.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

const envUsers = "USERS"

// applyUsersEnv merges the USERS env var (a JSON array of UserConfig) over
// the YAML users list, winning per path.
func applyUsersEnv(cfg *Config, strict bool) error {
	raw := strings.TrimSpace(os.Getenv(envUsers))
	if raw == "" {
		return nil
	}
	var fromEnv []UserConfig
	if err := decodeIaCJSON(envUsers, raw, &fromEnv, strict); err != nil {
		return fmt.Errorf("invalid %s: %w", envUsers, err)
	}
	cfg.Users = mergeByKey(cfg.Users, fromEnv, func(user UserConfig) string {
		return canonicalTextKey(user.Path)
	})
	return nil
}
