package pluginapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Config reads an instance configuration key by key, with the coercions a
// value needs whether it came from the dashboard form or from config.yaml:
// numbers written as strings, lists written as comma-separated text, bools
// written as yes/no. Every reader returns its default when the key is
// absent, null, or invalid, and the first problem is kept for [Config.Err],
// so a decoder reads all keys in a row and checks once:
//
//	cfg, err := pluginapi.ParseConfig(Name, p.Manifest().ConfigSchema, raw)
//	if err != nil {
//		return err
//	}
//	s.model = cfg.String("model", "")
//	s.action = cfg.Choice("action", "block", "block", "respond", "warn")
//	s.maxTokens = cfg.Int("max_tokens", 256, 1, 1<<20)
//	return cfg.Err()
type Config struct {
	plugin string
	values map[string]json.RawMessage
	err    error
}

// ParseConfig decodes raw, which may be empty or null for an empty
// configuration. A key that is not in schema is an error, so a typo in
// config.yaml is reported instead of ignored. Error messages start with the
// plugin name.
func ParseConfig(plugin string, schema []Field, raw json.RawMessage) (*Config, error) {
	c := &Config{plugin: plugin, values: map[string]json.RawMessage{}}
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c.values); err != nil {
		return nil, fmt.Errorf("%s: invalid config: %w", plugin, err)
	}
	for key := range c.values {
		if !hasField(schema, key) {
			return nil, fmt.Errorf("%s: invalid config: unknown field %q", plugin, key)
		}
	}
	return c, nil
}

func hasField(schema []Field, key string) bool {
	for _, f := range schema {
		if f.Key == key {
			return true
		}
	}
	return false
}

// Err returns the first problem a reader found, or nil.
func (c *Config) Err() error { return c.err }

func (c *Config) fail(key, format string, args ...any) {
	if c.err == nil {
		c.err = fmt.Errorf("%s: %s %s", c.plugin, key, fmt.Sprintf(format, args...))
	}
}

// Raw returns the value of key as stored, or nil when absent or null.
func (c *Config) Raw(key string) json.RawMessage {
	raw, ok := c.values[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	return raw
}

// String reads a string; absent or null keeps def.
func (c *Config) String(key, def string) string {
	raw := c.Raw(key)
	if raw == nil {
		return def
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		c.fail(key, "must be a string")
		return def
	}
	return s
}

// Choice reads a select value; absent, null, or "" keeps def, anything else
// must be one of allowed.
func (c *Config) Choice(key, def string, allowed ...string) string {
	s := c.String(key, def)
	if s == "" {
		return def
	}
	if !slices.Contains(allowed, s) {
		c.fail(key, "must be one of %s; got %q", strings.Join(allowed, ", "), s)
		return def
	}
	return s
}

// Bool reads a bool, also written as true/false, yes/no, on/off, or 1/0;
// absent, null, or "" is false.
func (c *Config) Bool(key string) bool {
	raw := c.Raw(key)
	if raw == nil {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "on", "1":
			return true
		case "false", "no", "off", "0", "":
			return false
		}
	}
	c.fail(key, "must be true or false, got %s", raw)
	return false
}

// number reads a JSON number or a numeric string. ok is false when the key
// is absent, null, or "".
func (c *Config) number(key string) (f float64, ok bool) {
	raw := c.Raw(key)
	if raw == nil {
		return 0, false
	}
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		c.fail(key, "must be a number, got %s", raw)
		return 0, false
	}
	if strings.TrimSpace(s) == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		c.fail(key, "must be a number, got %q", s)
		return 0, false
	}
	return f, true
}

// Float reads a number within [lo, hi]; absent, null, or "" keeps def.
func (c *Config) Float(key string, def, lo, hi float64) float64 {
	f, ok := c.number(key)
	if !ok {
		return def
	}
	if f < lo || f > hi {
		c.fail(key, "must be between %v and %v, got %v", lo, hi, f)
		return def
	}
	return f
}

// OptionalFloat reads a number within [lo, hi]; absent, null, or "" is nil.
func (c *Config) OptionalFloat(key string, lo, hi float64) *float64 {
	f, ok := c.number(key)
	if !ok {
		return nil
	}
	if f < lo || f > hi {
		c.fail(key, "must be between %v and %v, got %v", lo, hi, f)
		return nil
	}
	return &f
}

// Int reads a whole number within [lo, hi]; absent, null, or "" keeps def.
func (c *Config) Int(key string, def, lo, hi int) int {
	f, ok := c.number(key)
	if !ok {
		return def
	}
	if f != float64(int(f)) {
		c.fail(key, "must be a whole number, got %v", f)
		return def
	}
	n := int(f)
	if n < lo || n > hi {
		c.fail(key, "must be between %d and %d, got %d", lo, hi, n)
		return def
	}
	return n
}

// BlockStatus reads an HTTP status for [Decision.Status]: empty means the
// phase default (0), anything else must be between 400 and 599.
func (c *Config) BlockStatus(key string) int {
	n := c.Int(key, 0, 0, 1<<20)
	if n != 0 && (n < 400 || n > 599) {
		c.fail(key, "must be an HTTP status between 400 and 599, got %d", n)
		return 0
	}
	return n
}

// List reads a list of strings: a JSON array, or one string split on commas
// and newlines. Items are trimmed and blanks dropped. Absent or null is nil;
// an empty list is an empty, non-nil slice.
func (c *Config) List(key string) []string {
	raw := c.Raw(key)
	if raw == nil {
		return nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			c.fail(key, "must be a list of strings")
			return nil
		}
		items = strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' })
	}
	out := []string{}
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Lines reads a textarea as lines: one string split on newlines, or a JSON
// array of strings. Lines are kept as written. Absent or null is nil.
func (c *Config) Lines(key string) []string {
	raw := c.Raw(key)
	if raw == nil {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.Split(text, "\n")
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		c.fail(key, "must be text or a list of strings")
		return nil
	}
	return items
}

// Roles reads a checkbox list of roles (system, user, assistant, tool) into
// a set; "system" also selects [RoleDeveloper], the Responses spelling of
// system. Absent or null selects def.
func (c *Config) Roles(key string, def ...Role) map[Role]bool {
	items := c.List(key)
	if items == nil {
		roles := make(map[Role]bool, len(def)+1)
		for _, r := range def {
			roles[r] = true
			if r == RoleSystem {
				roles[RoleDeveloper] = true
			}
		}
		return roles
	}
	roles := make(map[Role]bool, len(items)+1)
	for _, item := range items {
		r := Role(strings.ToLower(item))
		if !slices.Contains(configRoles, r) {
			c.fail(key, "has unknown role %q (use system, user, assistant, tool)", item)
			continue
		}
		roles[r] = true
		if r == RoleSystem {
			roles[RoleDeveloper] = true
		}
	}
	return roles
}

var configRoles = []Role{RoleSystem, RoleUser, RoleAssistant, RoleTool}

// RoleOptions are the checkbox options for a roles field read by
// [Config.Roles].
func RoleOptions() []Option {
	return []Option{
		{Value: string(RoleSystem), Label: "System (and developer)"},
		{Value: string(RoleUser), Label: "User"},
		{Value: string(RoleAssistant), Label: "Assistant"},
		{Value: string(RoleTool), Label: "Tool results"},
	}
}
