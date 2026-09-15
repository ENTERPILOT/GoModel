// Package tagreplace is the built-in tag_replace plugin: before the provider
// call it expands {{gomodel.<tag>}} placeholders in prompt text into request
// facts such as the resolved model, the user path, or the current date.
package tagreplace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "tag_replace"

// DefaultTimezone is the zone the date tags are rendered in.
const DefaultTimezone = "UTC"

// Plugin is one configured tag_replace instance.
type Plugin struct {
	roles    map[pluginapi.Role]bool
	location *time.Location
	now      func() time.Time
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin and its configuration form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Replaces {{gomodel.<tag>}} placeholders in prompts with request values such as the resolved model or the current date.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt},
		Mutates:     true,
		Guardrail:   true,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "roles", Label: "Prompt roles", Input: pluginapi.InputCheckboxes, Default: []string{"system"},
				Help:    "Which prompt messages tags are expanded in. Adding user lets callers read request values such as labels.",
				Options: pluginapi.RoleOptions(),
			},
			{
				Key: "timezone", Label: "Timezone", Input: pluginapi.InputText, Default: DefaultTimezone,
				Help:        "IANA timezone for the date, time, and datetime tags, for example Europe/Warsaw.",
				Placeholder: DefaultTimezone,
			},
		},
	}
}

// Init decodes and validates the instance configuration.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	roles, loc, err := decodeConfig(p.Manifest().ConfigSchema, raw)
	if err != nil {
		return err
	}
	p.roles, p.location, p.now = roles, loc, time.Now
	return nil
}

func decodeConfig(schema []pluginapi.Field, raw json.RawMessage) (map[pluginapi.Role]bool, *time.Location, error) {
	cfg, err := pluginapi.ParseConfig(Name, schema, raw)
	if err != nil {
		return nil, nil, err
	}
	roles := cfg.Roles("roles", pluginapi.RoleSystem)
	zone := strings.TrimSpace(cfg.String("timezone", DefaultTimezone))
	if err := cfg.Err(); err != nil {
		return nil, nil, err
	}
	if zone == "" {
		zone = DefaultTimezone
	}
	// LoadLocation accepts "Local", the host zone, which varies by deployment.
	if zone == "Local" {
		return nil, nil, fmt.Errorf("%s: timezone %q must be UTC or an IANA timezone", Name, zone)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: timezone %q is not a known IANA timezone", Name, zone)
	}
	return roles, loc, nil
}

// Close releases nothing; the plugin holds no resources.
func (p *Plugin) Close(context.Context) error { return nil }

// Summarize returns one line describing the instance for the dashboard list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	roles, loc, err := decodeConfig(p.Manifest().ConfigSchema, raw)
	if err != nil {
		return ""
	}
	var names []string
	for _, r := range []pluginapi.Role{pluginapi.RoleSystem, pluginapi.RoleUser, pluginapi.RoleAssistant, pluginapi.RoleTool} {
		if roles[r] {
			names = append(names, string(r))
		}
	}
	return fmt.Sprintf("%s messages, %s", strings.Join(names, ", "), loc)
}

// OnPrompt expands the tags in the text of the configured roles.
func (p *Plugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	values := newValues(x.Meta, p.now().In(p.location))
	total := 0
	for _, t := range x.Prompt.TextTargets() {
		if !p.roles[t.Role] {
			continue
		}
		out, n := expand(t.Text, values)
		if n == 0 {
			continue
		}
		if err := x.Prompt.SetTargetText(t, out); err != nil {
			return pluginapi.Decision{}, err
		}
		total += n
	}
	if total == 0 {
		return pluginapi.Allow(), nil
	}
	return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: map[string]any{"replacements": total}}, nil
}
