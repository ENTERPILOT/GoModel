package headeredit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
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
	RequestSet     []string
	RequestRemove  []string
	ResponseSet    []string
	ResponseAdd    []string
	ResponseRemove []string
	UpstreamSet    []string
}

func decodeConfig(raw json.RawMessage) (config, error) {
	cfg, err := pluginapi.ParseConfig(Name, New().Manifest().ConfigSchema, raw)
	if err != nil {
		return config{}, err
	}
	c := config{
		RequestSet:     cfg.Lines("request_set"),
		RequestRemove:  cfg.Lines("request_remove"),
		ResponseSet:    cfg.Lines("response_set"),
		ResponseAdd:    cfg.Lines("response_add"),
		ResponseRemove: cfg.Lines("response_remove"),
		UpstreamSet:    cfg.Lines("upstream_set"),
	}
	return c, cfg.Err()
}

// errRefusedHeader marks a well-formed line naming a header the plugin may
// not edit. Such a line is skipped, not an error: see Plugin.Init.
var errRefusedHeader = errors.New("refused header")

// parseEdits turns the lines of one config field into edits. Blank lines and
// lines starting with # are ignored. Set and add lines look like
// "Name: value"; remove lines are a bare "Name". A line naming a header the
// plugin may not edit, or for an upstream field one the provider call merge
// would drop, is skipped and reported to ignored.
func parseEdits(field string, entries []string, o op, upstream bool, ignored func(field string, line int, err error)) ([]edit, error) {
	var out []edit
	for i, line := range entries {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseLine(line, o)
		if err == nil && upstream && core.IsPluginUpstreamHeaderDenied(e.name) {
			err = fmt.Errorf("header %q is set by the gateway and cannot be sent upstream: %w", e.name, errRefusedHeader)
		}
		if errors.Is(err, errRefusedHeader) {
			if ignored != nil {
				ignored(field, i+1, err)
			}
			continue
		}
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
	if core.IsCredentialHeader(lower) {
		return fmt.Errorf("header %q carries credentials and cannot be edited: %w", name, errRefusedHeader)
	}
	if o != opRemove && (strings.Contains(lower, "secret") || strings.Contains(lower, "token")) {
		return fmt.Errorf("header %q looks like a credential and cannot be set: %w", name, errRefusedHeader)
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
