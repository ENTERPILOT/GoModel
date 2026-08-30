package config

import (
	"fmt"
	"os"
	"strings"
)

// UserConfig declares one user-path access policy in config.yaml or the USERS
// env var, so model access can be managed as infrastructure-as-code. A
// declared policy shadows the admin-store row of the same path and is
// read-only in the dashboard.
type UserConfig struct {
	// Path is the user path (group or user) the policy applies to, e.g.
	// "/acme/eng". Deeper paths inherit it.
	Path string `yaml:"path" json:"path"`

	// AllowedModels lists the model selectors requests under Path may use:
	// exact "provider/model", provider-wide "provider/*", or model-wide
	// "model". Empty means the node itself does not restrict models.
	AllowedModels []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`

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
