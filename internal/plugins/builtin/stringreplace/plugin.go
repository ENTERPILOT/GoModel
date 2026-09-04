// Package stringreplace is the built-in string_replace plugin: it rewrites,
// flags, or blocks prompt and completion text that matches a list of literal
// or regular-expression rules, in place for non-streaming responses and in
// flight for streams. It doubles as a reference implementation of a
// mutating pluginapi plugin with prompt, response, and stream hooks.
package stringreplace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "string_replace"

var instances atomic.Int64

// Plugin is one configured string_replace instance.
type Plugin struct {
	key string // Exchange.Values key prefix, unique per instance
	settings
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin and its configuration form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Replaces, flags, or blocks text matching literal or regex rules in prompts, responses, and streams.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates:     true,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "rules", Label: "Rules", Input: pluginapi.InputTextarea, Required: true,
				Help:        "One rule per line as \"find => replace\" (the separator is \" => \" with spaces; the replacement may be empty). \\n, \\t, and \\\\ are understood on both sides in literal mode and on the replace side in regex mode. Rules apply in order; blank lines and lines starting with # are ignored.",
				Placeholder: "ACME Corp => [company]\n# regex mode: (\\d{3})-\\d{4} => $1-XXXX",
			},
			{
				Key: "mode", Label: "Mode", Input: pluginapi.InputSelect, Default: ModeLiteral,
				Help: "Literal matches the find text as written. Regex uses Go RE2 syntax; $1, $2 in the replacement refer to capture groups (write $$ for a literal dollar sign).",
				Options: []pluginapi.Option{
					{Value: ModeLiteral, Label: "Literal"},
					{Value: ModeRegex, Label: "Regular expression"},
				},
			},
			{
				Key: "case_insensitive", Label: "Case insensitive", Input: pluginapi.InputSelect, Default: "false",
				Help: "Match regardless of letter case.",
				Options: []pluginapi.Option{
					{Value: "false", Label: "No"},
					{Value: "true", Label: "Yes"},
				},
			},
			{
				Key: "roles", Label: "Prompt roles", Input: pluginapi.InputCheckboxes, Default: []string{"user"},
				Help:    "Which prompt messages the rules apply to. In the response phase the assistant text is always the target.",
				Options: roleOptions,
			},
			{
				Key: "on_match", Label: "On match", Input: pluginapi.InputSelect, Default: OnMatchReplace,
				Help: "Replace performs the substitution. Block rejects with an error, respond answers with the message as an assistant reply, and warn continues while recording the match; none of these three edits the text.",
				Options: []pluginapi.Option{
					{Value: OnMatchReplace, Label: "Replace"},
					{Value: OnMatchBlock, Label: "Block"},
					{Value: OnMatchRespond, Label: "Respond"},
					{Value: OnMatchWarn, Label: "Warn"},
				},
			},
			{
				Key: "message", Label: "Message", Input: pluginapi.InputText, Default: DefaultMessage,
				Help:        "Error message for block, assistant reply for respond, and audit note for warn.",
				Placeholder: DefaultMessage,
			},
			{
				Key: "block_status", Label: "Block status", Input: pluginapi.InputNumber,
				Help:        "HTTP status for block (400 to 599). Leave empty for the phase default: 400 when a prompt is blocked, 502 when a response is blocked.",
				Placeholder: "400 (prompt) / 502 (response)",
			},
			{
				Key: "stream_lookbehind", Label: "Stream lookbehind", Input: pluginapi.InputNumber, Default: DefaultStreamLookbehind,
				Help:        "Characters of streamed text held back so a match that spans two chunks is still rewritten; set it to at least the longest find text. Used by replace and warn. Block and respond buffer the whole stream instead, so nothing leaks before the decision, at the cost of delaying the first token until the response is complete.",
				Placeholder: fmt.Sprint(DefaultStreamLookbehind),
			},
		},
	}
}

// Init decodes and validates the instance configuration.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	s, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	p.settings = s
	p.key = fmt.Sprintf("%s:%d", Name, instances.Add(1))
	return nil
}

// Close releases nothing; the plugin holds no resources.
func (p *Plugin) Close(context.Context) error { return nil }

// Summarize returns one line describing the instance for the dashboard list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	s, err := decodeConfig(raw)
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s rule", len(s.rules), s.mode)
	if len(s.rules) != 1 {
		b.WriteString("s")
	}
	if s.caseInsensitive {
		b.WriteString(" (case-insensitive)")
	}
	fmt.Fprintf(&b, ", %s", s.onMatch)
	return b.String()
}
