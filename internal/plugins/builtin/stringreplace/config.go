package stringreplace

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Mode values for the "mode" config key.
const (
	ModeLiteral = "literal"
	ModeRegex   = "regex"
)

// OnMatch values for the "on_match" config key.
const (
	OnMatchReplace = "replace"
	OnMatchBlock   = "block"
	OnMatchRespond = "respond"
	OnMatchWarn    = "warn"
)

// Defaults for optional config keys.
const (
	DefaultMessage          = "Request blocked by policy"
	DefaultStreamLookbehind = 64
)

var roleOptions = []pluginapi.Option{
	{Value: "system", Label: "System (and developer)"},
	{Value: "user", Label: "User"},
	{Value: "assistant", Label: "Assistant"},
	{Value: "tool", Label: "Tool results"},
}

// config is the instance configuration as stored by the host. Values are
// kept raw so each key can be decoded with a message naming the key.
type config struct {
	Rules            json.RawMessage `json:"rules"`
	Mode             json.RawMessage `json:"mode"`
	CaseInsensitive  json.RawMessage `json:"case_insensitive"`
	Roles            json.RawMessage `json:"roles"`
	OnMatch          json.RawMessage `json:"on_match"`
	Message          json.RawMessage `json:"message"`
	BlockStatus      json.RawMessage `json:"block_status"`
	StreamLookbehind json.RawMessage `json:"stream_lookbehind"`
}

// settings is the validated configuration.
type settings struct {
	rules           []rule
	mode            string
	caseInsensitive bool
	roles           map[pluginapi.Role]bool
	onMatch         string
	message         string
	blockStatus     int
	lookbehind      int
}

func decodeConfig(raw json.RawMessage) (settings, error) {
	var cfg config
	if len(strings.TrimSpace(string(raw))) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return settings{}, fmt.Errorf("%s: invalid config: %w", Name, err)
		}
	}
	s := settings{
		mode:       ModeLiteral,
		onMatch:    OnMatchReplace,
		message:    DefaultMessage,
		lookbehind: DefaultStreamLookbehind,
		roles:      map[pluginapi.Role]bool{pluginapi.RoleUser: true},
	}
	var err error
	if s.mode, err = parseChoice("mode", cfg.Mode, s.mode, ModeLiteral, ModeRegex); err != nil {
		return settings{}, err
	}
	if s.caseInsensitive, err = parseBool("case_insensitive", cfg.CaseInsensitive); err != nil {
		return settings{}, err
	}
	if s.onMatch, err = parseChoice("on_match", cfg.OnMatch, s.onMatch, OnMatchReplace, OnMatchBlock, OnMatchRespond, OnMatchWarn); err != nil {
		return settings{}, err
	}
	if s.message, err = parseString("message", cfg.Message, s.message); err != nil {
		return settings{}, err
	}
	if s.blockStatus, err = parseInt("block_status", cfg.BlockStatus, 0, 0, 599); err != nil {
		return settings{}, err
	}
	if s.blockStatus != 0 && s.blockStatus < 400 {
		return settings{}, fmt.Errorf("%s: block_status must be an HTTP status between 400 and 599, got %d", Name, s.blockStatus)
	}
	if s.lookbehind, err = parseInt("stream_lookbehind", cfg.StreamLookbehind, s.lookbehind, 0, 1<<20); err != nil {
		return settings{}, err
	}
	roles, err := parseList("roles", cfg.Roles)
	if err != nil {
		return settings{}, err
	}
	if roles != nil {
		s.roles = map[pluginapi.Role]bool{}
		for _, r := range roles {
			if !validRole(r) {
				return settings{}, fmt.Errorf("%s: roles: unknown role %q (use system, user, assistant, tool)", Name, r)
			}
			s.roles[pluginapi.Role(r)] = true
			if r == "system" {
				s.roles[pluginapi.RoleDeveloper] = true
			}
		}
	}
	ruleLines, err := parseLines("rules", cfg.Rules)
	if err != nil {
		return settings{}, err
	}
	s.rules, err = parseRules(ruleLines, s.mode, s.caseInsensitive)
	if err != nil {
		return settings{}, err
	}
	if len(s.rules) == 0 {
		return settings{}, fmt.Errorf("%s: rules is required: add at least one \"find => replace\" line", Name)
	}
	return s, nil
}

func validRole(r string) bool {
	for _, o := range roleOptions {
		if o.Value == r {
			return true
		}
	}
	return false
}

func isUnset(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// parseString decodes a JSON string; absent or null keeps def.
func parseString(key string, raw json.RawMessage, def string) (string, error) {
	if isUnset(raw) {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %s must be a string", Name, key)
	}
	return s, nil
}

// parseChoice decodes a select value; absent, null, or "" keeps def.
func parseChoice(key string, raw json.RawMessage, def string, allowed ...string) (string, error) {
	s, err := parseString(key, raw, def)
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	if slices.Contains(allowed, s) {
		return s, nil
	}
	return "", fmt.Errorf("%s: %s must be one of %s; got %q", Name, key, strings.Join(allowed, ", "), s)
}

// parseBool accepts a JSON bool or the strings true/false/yes/no; absent,
// null, or "" is false.
func parseBool(key string, raw json.RawMessage) (bool, error) {
	if isUnset(raw) {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off", "":
			return false, nil
		}
	}
	return false, fmt.Errorf("%s: %s must be true or false, got %s", Name, key, raw)
}

// parseInt accepts a JSON number or a numeric string within [lo, hi];
// absent, null, or "" keeps def.
func parseInt(key string, raw json.RawMessage, def, lo, hi int) (int, error) {
	if isUnset(raw) {
		return def, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, fmt.Errorf("%s: %s must be a number, got %s", Name, key, raw)
		}
		if strings.TrimSpace(s) == "" {
			return def, nil
		}
		if f, err = strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
			return 0, fmt.Errorf("%s: %s must be a number, got %q", Name, key, s)
		}
	}
	n := int(f)
	if float64(n) != f || n < lo || n > hi {
		return 0, fmt.Errorf("%s: %s must be a whole number between %d and %d, got %v", Name, key, lo, hi, f)
	}
	return n, nil
}

// parseLines decodes a textarea: a JSON string split on newlines or a JSON
// array of strings. Absent or null returns nil.
func parseLines(key string, raw json.RawMessage) ([]string, error) {
	if isUnset(raw) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.Split(text, "\n"), nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%s: %s must be text or a list of strings", Name, key)
	}
	return items, nil
}

// parseList decodes a checkboxes value: a JSON array of strings or one
// comma-separated string. Absent or null returns nil; an empty list is
// returned as an empty (non-nil) slice.
func parseList(key string, raw json.RawMessage) ([]string, error) {
	if isUnset(raw) {
		return nil, nil
	}
	items := []string{}
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("%s: %s must be a list of strings", Name, key)
	}
	for item := range strings.SplitSeq(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items, nil
}
