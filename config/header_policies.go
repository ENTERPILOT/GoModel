package config

import (
	"fmt"
	"strings"
)

// HeaderPoliciesConfig configures reusable outbound request-header policies.
type HeaderPoliciesConfig struct {
	// Enabled is an operational kill switch. With no policies configured, the
	// enabled default has no effect on requests.
	Enabled bool `yaml:"enabled" env:"HEADER_POLICIES_ENABLED"`

	// Policies seeds named definitions and binds them to the managed default
	// workflow using each policy's Step.
	Policies []HeaderPolicyConfig `yaml:"policies"`

	// PoliciesJSON replaces Policies when HEADER_POLICIES_JSON is set.
	PoliciesJSON string `yaml:"-" env:"HEADER_POLICIES_JSON"`
}

// HeaderPolicyConfig is one declarative outbound header policy.
type HeaderPolicyConfig struct {
	Name        string                  `json:"name" yaml:"name"`
	Description string                  `json:"description,omitempty" yaml:"description"`
	Step        int                     `json:"step,omitempty" yaml:"step"`
	Methods     []string                `json:"methods,omitempty" yaml:"methods"`
	Paths       []string                `json:"paths,omitempty" yaml:"paths"`
	When        []HeaderConditionConfig `json:"when,omitempty" yaml:"when"`
	Actions     []HeaderActionConfig    `json:"actions" yaml:"actions"`
}

func applyHeaderPoliciesEnv(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	raw := strings.TrimSpace(cfg.HeaderPolicies.PoliciesJSON)
	if raw == "" {
		return nil
	}
	raw = expandString(raw)
	var policies []HeaderPolicyConfig
	if err := decodeStrictJSON(raw, &policies); err != nil {
		return fmt.Errorf("invalid HEADER_POLICIES_JSON: %w", err)
	}
	cfg.HeaderPolicies.Policies = policies
	return nil
}
