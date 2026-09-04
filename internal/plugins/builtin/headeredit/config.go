package headeredit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// op is what an edit does to a header.
type op string

const (
	opSet    op = "set"
	opAdd    op = "add"
	opRemove op = "remove"
)

// edit is one parsed header change.
type edit struct {
	op    op
	name  string // canonical header name
	value string
}

// config is the instance configuration as stored by the host. Every field
// is a textarea holding one entry per line; values stay raw so decoding
// errors can name the key.
type config struct {
	RequestSet     json.RawMessage `json:"request_set"`
	RequestRemove  json.RawMessage `json:"request_remove"`
	ResponseSet    json.RawMessage `json:"response_set"`
	ResponseAdd    json.RawMessage `json:"response_add"`
	ResponseRemove json.RawMessage `json:"response_remove"`
	UpstreamSet    json.RawMessage `json:"upstream_set"`
}

// parseLines decodes a textarea value: a JSON string (one entry per line)
// or a JSON array of strings.
func parseLines(key string, raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.Split(text, "\n"), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("header_edit: %s must be text or a list of strings", key)
	}
	return list, nil
}

// credentialHeaders can never be set, added, or removed by this plugin.
var credentialHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"api-key":             true,
	"x-goog-api-key":      true,
	"x-anthropic-api-key": true,
	"x-auth-token":        true,
}

func decodeConfig(raw json.RawMessage) (config, error) {
	var cfg config
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return cfg, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("header_edit: invalid config: %w", err)
	}
	return cfg, nil
}

// parseEdits turns the lines of one config field into edits. Blank lines and
// lines starting with # are ignored. Set and add lines look like
// "Name: value"; remove lines are a bare "Name".
func parseEdits(field string, raw json.RawMessage, o op) ([]edit, error) {
	entries, err := parseLines(field, raw)
	if err != nil {
		return nil, err
	}
	var out []edit
	for i, line := range entries {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseLine(line, o)
		if err != nil {
			return nil, fmt.Errorf("header_edit: %s line %d: %w", field, i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func parseLine(line string, o op) (edit, error) {
	name, value := line, ""
	if o != opRemove {
		before, after, ok := strings.Cut(line, ":")
		if !ok {
			return edit{}, fmt.Errorf("expected \"Name: value\", got %q", line)
		}
		name, value = strings.TrimSpace(before), strings.TrimSpace(after)
	} else if strings.Contains(line, ":") {
		return edit{}, fmt.Errorf("expected a bare header name, got %q", line)
	}
	if err := checkName(name, o); err != nil {
		return edit{}, err
	}
	return edit{op: o, name: http.CanonicalHeaderKey(name), value: value}, nil
}

func checkName(name string, o op) error {
	if name == "" {
		return fmt.Errorf("empty header name")
	}
	for _, r := range name {
		if !isTokenChar(r) {
			return fmt.Errorf("invalid header name %q", name)
		}
	}
	lower := strings.ToLower(name)
	if credentialHeaders[lower] {
		return fmt.Errorf("header %q carries credentials and cannot be edited", name)
	}
	if o != opRemove && (strings.Contains(lower, "secret") || strings.Contains(lower, "token")) {
		return fmt.Errorf("header %q looks like a credential and cannot be set", name)
	}
	return nil
}

// isTokenChar reports whether r is an RFC 9110 token character.
func isTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}
