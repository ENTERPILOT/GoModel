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

	// MaxAttempts caps how many failover targets one request may try after
	// its primary attempt fails. Zero (the default) sweeps every remaining
	// target in order.
	MaxAttempts int `yaml:"max_attempts" env:"FAILOVER_MAX_ATTEMPTS"`

	// RetryOnStatuses lists the upstream HTTP statuses that trigger failover:
	// exact codes (408) or whole classes (5xx). Setting it replaces the
	// default DefaultFailoverRetryStatuses; an empty list keeps the default.
	RetryOnStatuses []string `yaml:"retry_on_statuses" env:"FAILOVER_RETRY_ON_STATUSES"`

	// RetryOnErrors lists phrases that trigger failover regardless of status:
	// an error qualifies when every word of a phrase appears in its code or
	// message. A numeric word (404, 4xx) matches the HTTP status instead of
	// the text. Setting it replaces the default DefaultFailoverRetryErrors; an
	// empty list keeps the default.
	RetryOnErrors []string `yaml:"retry_on_errors" env:"FAILOVER_RETRY_ON_ERRORS"`

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

	// RetryStatuses is the parsed RetryOnStatuses (or its default): the set of
	// HTTP statuses that trigger failover.
	RetryStatuses map[int]bool `yaml:"-"`

	// RetryErrors is the parsed RetryOnErrors (or its default): one lower-cased
	// word list per phrase.
	RetryErrors []FailoverErrorPhrase `yaml:"-"`
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

	manual, err := failoverManualRules(cfg)
	if err != nil {
		return err
	}
	cfg.Manual = manual

	disabled, err := failoverDisabledModels(cfg)
	if err != nil {
		return err
	}
	cfg.Disabled = disabled
	return LoadFailoverPolicy(cfg)
}

// LoadFailoverPolicy validates the retry policy fields of cfg and fills the
// parsed RetryStatuses and RetryErrors, applying the defaults for empty lists.
func LoadFailoverPolicy(cfg *FailoverConfig) error {
	if cfg.MaxAttempts < 0 {
		return fmt.Errorf("failover.max_attempts: must be zero or positive, got %d", cfg.MaxAttempts)
	}
	statuses, err := parseFailoverRetryStatuses(cfg.RetryOnStatuses)
	if err != nil {
		return err
	}
	cfg.RetryStatuses = statuses
	phrases, err := parseFailoverRetryErrors(cfg.RetryOnErrors)
	if err != nil {
		return err
	}
	cfg.RetryErrors = phrases
	return nil
}

// failoverManualRules merges the three sources of manual rules, later sources
// overwriting a key an earlier one set: inline YAML, then the rules file, then
// the env JSON. It returns nil rather than an empty map when no source
// contributes, because callers treat a nil map as "no manual rules".
func failoverManualRules(cfg *FailoverConfig) (map[string][]string, error) {
	merged := make(map[string][]string)

	if err := mergeFailoverRules(merged, cfg.Rules, "failover.rules"); err != nil {
		return nil, err
	}

	fromFile, err := failoverRulesFromFile(cfg.ManualRulesPath)
	if err != nil {
		return nil, err
	}
	if err := mergeFailoverRules(merged, fromFile, "failover.manual_rules_path"); err != nil {
		return nil, err
	}

	// An unset env var is simply no rules; an empty *file*, by contrast, stays
	// a parse error, since configuring a path to nothing is a mistake.
	if strings.TrimSpace(cfg.RulesJSON) != "" {
		fromEnv, err := failoverRulesFromJSON(cfg.RulesJSON, "failover.rules_json")
		if err != nil {
			return nil, err
		}
		if err := mergeFailoverRules(merged, fromEnv, "failover.rules_json"); err != nil {
			return nil, err
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// failoverRulesFromFile reads manual rules from a JSON file, or returns nil
// when no path is configured.
func failoverRulesFromFile(path string) (map[string][]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failover.manual_rules_path: failed to read %q: %w", path, err)
	}
	return failoverRulesFromJSON(string(raw),
		fmt.Sprintf("failover.manual_rules_path: failed to parse %q", path))
}

// failoverRulesFromJSON decodes a JSON object mapping a source model to its
// ordered failover models.
func failoverRulesFromJSON(raw, label string) (map[string][]string, error) {
	entries, err := decodeStrictJSONObject(raw, label)
	if err != nil {
		return nil, err
	}
	rules := make(map[string][]string, len(entries))
	for key, rawModels := range entries {
		if bytes.Equal(bytes.TrimSpace(rawModels), []byte("null")) {
			return nil, fmt.Errorf("%s: null not allowed for %q", label, key)
		}
		var models []string
		if err := json.Unmarshal(rawModels, &models); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		rules[key] = models
	}
	return rules, nil
}

// decodeStrictJSONObject decodes a JSON object into its raw values, rejecting
// what a plain json.Unmarshal into a map would silently accept: a non-object
// top level, a repeated key (the last would win, quietly discarding a rule an
// operator wrote), and trailing content after the object (usually a truncated
// or concatenated file). Detecting those needs the token stream, which is why
// this is hand-rolled rather than an Unmarshal.
func decodeStrictJSONObject(raw, label string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(expandString(raw)))

	if err := expectJSONDelim(decoder, '{', label); err != nil {
		return nil, err
	}

	entries := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s: object key must be a string", label)
		}
		if _, exists := entries[key]; exists {
			return nil, fmt.Errorf("%s: duplicate JSON key %q", label, key)
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		entries[key] = value
	}

	if err := expectJSONDelim(decoder, '}', label); err != nil {
		return nil, err
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		return nil, fmt.Errorf("%s: unexpected trailing JSON content", label)
	}
	return entries, nil
}

func expectJSONDelim(decoder *json.Decoder, want json.Delim, label string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != want {
		return fmt.Errorf("%s: top-level JSON value must be an object", label)
	}
	return nil
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
	addDisabledModel(disabled, cfg.DisabledModels...)

	raw := strings.TrimSpace(cfg.DisabledModelsJSON)
	if raw == "" {
		return nilIfEmpty(disabled), nil
	}
	if err := addDisabledModelsFromJSON(disabled, raw); err != nil {
		return nil, err
	}
	return nilIfEmpty(disabled), nil
}

// addDisabledModelsFromJSON accepts either spelling operators use: a plain
// array of selectors, or an object mapping a selector to a boolean so a single
// entry can be turned off without deleting it.
func addDisabledModelsFromJSON(disabled map[string]bool, raw string) error {
	expanded := expandString(raw)
	if strings.TrimSpace(expanded) == "null" {
		return fmt.Errorf("disabled models JSON: null not allowed; expected an array or object")
	}

	var list []string
	if err := json.Unmarshal([]byte(expanded), &list); err == nil {
		addDisabledModel(disabled, list...)
		return nil
	}

	var keyed map[string]bool
	if err := json.Unmarshal([]byte(expanded), &keyed); err != nil {
		return fmt.Errorf("failover.disabled_models_json: must be a JSON array or boolean object: %w", err)
	}
	for model, isDisabled := range keyed {
		if isDisabled {
			addDisabledModel(disabled, model)
		}
	}
	return nil
}

func addDisabledModel(disabled map[string]bool, models ...string) {
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			disabled[model] = true
		}
	}
}

func nilIfEmpty(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	return m
}
