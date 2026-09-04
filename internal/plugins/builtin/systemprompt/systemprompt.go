// Package systemprompt is the built-in system_prompt plugin: it injects,
// overrides, or decorates the system message before the provider call.
package systemprompt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the plugin's manifest name.
const Name = "system_prompt"

// Mode selects how the configured content is applied.
type Mode string

const (
	// ModeInject adds a system message only if none exists.
	ModeInject Mode = "inject"
	// ModeOverride replaces every system and developer message with one.
	ModeOverride Mode = "override"
	// ModeDecorator prepends the content to the first system message, or
	// injects one when there is none.
	ModeDecorator Mode = "decorator"
)

// Config is the instance configuration.
type Config struct {
	Mode    string `json:"mode"`
	Content string `json:"content"`
}

// Plugin implements the prompt hook.
type Plugin struct {
	mode    Mode
	content string
}

// New returns a fresh plugin value.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Injects, overrides, or decorates the system message before the request reaches the provider.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt},
		Mutates:     true,
		ConfigSchema: []pluginapi.Field{
			{
				Key:      "mode",
				Label:    "Mode",
				Input:    pluginapi.InputSelect,
				Required: true,
				Help:     "Choose whether the prompt is injected only when absent, overrides existing system prompts, or decorates the first one.",
				Default:  string(ModeInject),
				Options: []pluginapi.Option{
					{Value: string(ModeInject), Label: "Inject"},
					{Value: string(ModeOverride), Label: "Override"},
					{Value: string(ModeDecorator), Label: "Decorator"},
				},
			},
			{
				Key:         "content",
				Label:       "Content",
				Input:       pluginapi.InputTextarea,
				Required:    true,
				Help:        "The system prompt text applied by this guardrail.",
				Placeholder: "You are a precise assistant. Follow the compliance policy...",
			},
		},
	}
}

// Init parses and validates the config.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	p.mode = Mode(cfg.Mode)
	p.content = cfg.Content
	return nil
}

// Close releases nothing.
func (p *Plugin) Close(context.Context) error { return nil }

// ParseConfig decodes and validates a config: mode defaults to inject and
// content is required.
func ParseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("invalid system_prompt config: %w", err)
		}
	}
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	if cfg.Mode == "" {
		cfg.Mode = string(ModeInject)
	}
	switch Mode(cfg.Mode) {
	case ModeInject, ModeOverride, ModeDecorator:
	default:
		return Config{}, fmt.Errorf("invalid system prompt mode: %q (must be inject, override, or decorator)", cfg.Mode)
	}
	cfg.Content = strings.TrimSpace(cfg.Content)
	if cfg.Content == "" {
		return Config{}, fmt.Errorf("system prompt content cannot be empty")
	}
	return cfg, nil
}

// OnPrompt applies the configured mode to the prompt's system messages.
func (p *Plugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	switch p.mode {
	case ModeOverride:
		if err := p.override(x.Prompt); err != nil {
			return pluginapi.Decision{}, err
		}
	case ModeDecorator:
		if err := p.decorate(x.Prompt); err != nil {
			return pluginapi.Decision{}, err
		}
	default:
		p.inject(x.Prompt)
	}
	return pluginapi.Allow(), nil
}

// inject adds a system message at the beginning only if none exists.
func (p *Plugin) inject(prompt *pluginapi.Prompt) {
	if firstSystem(prompt) != nil {
		return
	}
	prompt.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, p.content))
}

// override replaces every system message with a single one at the beginning.
// A leading single-text system message is edited in place so it keeps its
// identity (for Responses, the instructions field); any other system
// messages are removed.
func (p *Plugin) override(prompt *pluginapi.Prompt) error {
	var ids []string
	for _, m := range prompt.Messages {
		if isSystem(m) {
			ids = append(ids, m.ID)
		}
	}
	keepFirst := len(prompt.Messages) > 0 && isSystem(prompt.Messages[0]) && isSingleText(prompt.Messages[0])
	if keepFirst {
		if err := prompt.SetText(prompt.Messages[0].ID, 0, p.content); err != nil {
			return err
		}
		ids = ids[1:]
	}
	for _, id := range ids {
		if err := prompt.Remove(id); err != nil {
			return err
		}
	}
	if !keepFirst {
		prompt.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, p.content))
	}
	return nil
}

func isSingleText(m pluginapi.Message) bool {
	return len(m.Parts) == 1 && m.Parts[0].Kind == pluginapi.PartText
}

// decorate prepends the content to the first system message's first text
// part, or injects a new system message when there is none.
func (p *Plugin) decorate(prompt *pluginapi.Prompt) error {
	m := firstSystem(prompt)
	if m == nil {
		prompt.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, p.content))
		return nil
	}
	for i, part := range m.Parts {
		if part.Kind == pluginapi.PartText {
			return prompt.SetText(m.ID, i, p.content+"\n"+part.Text)
		}
	}
	// No text part to decorate: replace the message with a text one that
	// keeps its position and role.
	index := indexOf(prompt, m.ID)
	replacement := pluginapi.Message{Role: m.Role, Name: m.Name, Parts: append([]pluginapi.Part{{Kind: pluginapi.PartText, Text: p.content}}, m.Parts...)}
	if err := prompt.Remove(m.ID); err != nil {
		return err
	}
	prompt.Insert(index, replacement)
	return nil
}

func firstSystem(prompt *pluginapi.Prompt) *pluginapi.Message {
	for i := range prompt.Messages {
		if isSystem(prompt.Messages[i]) {
			return &prompt.Messages[i]
		}
	}
	return nil
}

func isSystem(m pluginapi.Message) bool {
	return m.Role == pluginapi.RoleSystem || m.Role == pluginapi.RoleDeveloper
}

func indexOf(prompt *pluginapi.Prompt, id string) int {
	for i := range prompt.Messages {
		if prompt.Messages[i].ID == id {
			return i
		}
	}
	return 0
}

// Summarize renders the one-line summary shown in the guardrails list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return ""
	}
	content := strings.Join(strings.Fields(cfg.Content), " ")
	const maxLen = 72
	if len(content) > maxLen {
		content = content[:maxLen-3] + "..."
	}
	if content == "" {
		return cfg.Mode
	}
	return fmt.Sprintf("%s • %s", cfg.Mode, content)
}
