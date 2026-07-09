package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type FailoverMode string

const (
	FailoverModeOff    FailoverMode = "off"
	FailoverModeManual FailoverMode = "manual"
	FailoverModeAuto   FailoverMode = "auto"
)

// Valid reports whether mode is one of the supported failover modes.
func (m FailoverMode) Valid() bool {
	switch normalizeFailoverMode(m) {
	case FailoverModeOff, FailoverModeManual, FailoverModeAuto:
		return true
	default:
		return false
	}
}

func normalizeFailoverMode(mode FailoverMode) FailoverMode {
	return FailoverMode(strings.ToLower(strings.TrimSpace(string(mode))))
}

// ResolveFailoverDefaultMode canonicalizes the global failover default mode and
// applies the process default when unset.
func ResolveFailoverDefaultMode(mode FailoverMode) FailoverMode {
	mode = normalizeFailoverMode(mode)
	if mode == "" {
		return FailoverModeManual
	}
	return mode
}

// FailoverConfig holds translated-route model failover policy.
type FailoverConfig struct {
	// Enabled controls failover globally. It defaults to true; configured rules
	// and workflow policy decide whether any request has failover candidates.
	Enabled bool `yaml:"enabled" env:"FAILOVER_ENABLED"`

	// DefaultMode is a deprecated compatibility field. It is accepted from old
	// config files and FAILOVER_MODE, but runtime failover is manual-only.
	DefaultMode FailoverMode `yaml:"default_mode" env:"FAILOVER_MODE"`

	// ManualRulesPath points to a JSON file that maps source model selectors to
	// ordered failover model selector lists. Empty disables manual rules.
	ManualRulesPath string `yaml:"manual_rules_path" env:"FAILOVER_MANUAL_RULES_PATH"`

	// Rules defines manual failover rules inline in config.yaml.
	Rules map[string][]string `yaml:"rules"`

	// RulesJSON defines manual failover rules inline from env.
	RulesJSON string `yaml:"rules_json" env:"FAILOVER_RULES_JSON"`

	// DisabledModels disables failover for matching source selectors.
	DisabledModels []string `yaml:"disabled_models" env:"FAILOVER_DISABLED_MODELS"`

	// DisabledModelsJSON disables failover for matching source selectors from
	// env. It accepts either a JSON string array or object with boolean values.
	DisabledModelsJSON string `yaml:"disabled_models_json" env:"FAILOVER_DISABLED_MODELS_JSON"`

	// Overrides is a removed compatibility field. Per-model failover modes are gone;
	// operators migrate to DisabledModels. It is still parsed — and ignored, with a
	// warning — so an old config file keeps booting under strict YAML validation.
	Overrides map[string]any `yaml:"overrides"`

	// Manual holds the parsed manual failover lists loaded from ManualRulesPath.
	Manual map[string][]string `yaml:"-"`

	// Disabled holds normalized per-model failover disables.
	Disabled map[string]bool `yaml:"-"`
}

func loadFailoverConfig(cfg *FailoverConfig) error {
	if cfg == nil {
		return nil
	}

	if len(cfg.Overrides) > 0 {
		slog.Warn("failover.overrides was removed and is ignored; use failover.disabled_models instead")
		cfg.Overrides = nil
	}

	cfg.DefaultMode = ResolveFailoverDefaultMode(cfg.DefaultMode)

	manual := make(map[string][]string)
	if err := mergeFailoverRules(manual, cfg.Rules, "failover.rules"); err != nil {
		return err
	}

	path := strings.TrimSpace(cfg.ManualRulesPath)
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failover.manual_rules_path: failed to read %q: %w", path, err)
		}
		decoded, err := decodeFailoverRuleJSON(string(raw), fmt.Sprintf("failover.manual_rules_path: failed to parse %q", path))
		if err != nil {
			return err
		}
		if err := mergeFailoverRules(manual, decoded, "failover.manual_rules_path"); err != nil {
			return err
		}
	}

	if inline := strings.TrimSpace(cfg.RulesJSON); inline != "" {
		decoded, err := decodeFailoverRuleJSON(inline, "failover.rules_json")
		if err != nil {
			return err
		}
		if err := mergeFailoverRules(manual, decoded, "failover.rules_json"); err != nil {
			return err
		}
	}

	cfg.Manual = nil
	if len(manual) > 0 {
		cfg.Manual = manual
	}

	disabled, err := failoverDisabledModels(cfg)
	if err != nil {
		return err
	}
	cfg.Disabled = disabled
	return nil
}

func decodeFailoverRuleJSON(raw, label string) (map[string][]string, error) {
	expanded := expandString(raw)
	decoded := make(map[string][]string)
	decoder := json.NewDecoder(strings.NewReader(expanded))

	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("%s: top-level JSON value must be an object", label)
	}

	seenKeys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s: object key must be a string", label)
		}
		if _, exists := seenKeys[key]; exists {
			return nil, fmt.Errorf("%s: duplicate JSON key %q", label, key)
		}
		seenKeys[key] = struct{}{}

		var rawModels json.RawMessage
		if err := decoder.Decode(&rawModels); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if bytes.Equal(bytes.TrimSpace(rawModels), []byte("null")) {
			return nil, fmt.Errorf("%s: null not allowed for %q", label, key)
		}
		var models []string
		if err := json.Unmarshal(rawModels, &models); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		decoded[key] = models
	}

	token, err = decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return nil, fmt.Errorf("%s: top-level JSON value must be an object", label)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		return nil, fmt.Errorf("%s: unexpected trailing JSON content", label)
	}
	return decoded, nil
}

func mergeFailoverRules(dst map[string][]string, src map[string][]string, label string) error {
	seen := make(map[string]struct{}, len(src))
	for key, models := range src {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%s: model key cannot be empty", label)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s: duplicate manual rule key after trimming: %q", label, key)
		}
		seen[key] = struct{}{}
		normalized := make([]string, 0, len(models))
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			normalized = append(normalized, model)
		}
		dst[key] = normalized
	}
	return nil
}

func failoverDisabledModels(cfg *FailoverConfig) (map[string]bool, error) {
	disabled := make(map[string]bool)
	for _, model := range cfg.DisabledModels {
		model = strings.TrimSpace(model)
		if model != "" {
			disabled[model] = true
		}
	}
	if raw := strings.TrimSpace(cfg.DisabledModelsJSON); raw != "" {
		expanded := expandString(raw)
		if strings.TrimSpace(expanded) == "null" {
			return nil, fmt.Errorf("disabled models JSON: null not allowed; expected an array or object")
		}
		var list []string
		if err := json.Unmarshal([]byte(expanded), &list); err == nil {
			for _, model := range list {
				model = strings.TrimSpace(model)
				if model != "" {
					disabled[model] = true
				}
			}
			return nilIfEmpty(disabled), nil
		}
		var keyed map[string]bool
		if err := json.Unmarshal([]byte(expanded), &keyed); err != nil {
			return nil, fmt.Errorf("failover.disabled_models_json: must be a JSON array or boolean object: %w", err)
		}
		for key, value := range keyed {
			key = strings.TrimSpace(key)
			if key != "" && value {
				disabled[key] = true
			}
		}
	}
	return nilIfEmpty(disabled), nil
}

func nilIfEmpty(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	return m
}
