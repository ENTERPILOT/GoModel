// Package llmjudge is the built-in llm_judge plugin: it asks a second model
// whether a prompt or a completion violates a policy and blocks, answers,
// or flags the exchange based on the verdict. It doubles as a reference
// implementation of a pluginapi plugin that uses Host.Inference and buffers
// streams.
package llmjudge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "llm_judge"

var instances atomic.Int64

// Plugin is one configured llm_judge instance.
type Plugin struct {
	key  string // Exchange.Values key prefix, unique per instance
	host pluginapi.Host
	settings
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin and its configuration form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Asks a judge model whether prompts and responses violate a policy, then blocks, answers, or flags them.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates:     false,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "model", Label: "Judge model", Input: pluginapi.InputModel, Required: true,
				Help:        "Model that judges the content: a \"provider/model\" reference, an alias, or a virtual model. Its usage is accounted to this gateway with origin \"plugin\".",
				Placeholder: "openai/gpt-4o-mini",
			},
			{
				Key: "user_path", Label: "User path", Input: pluginapi.InputText, Default: "",
				Help:        "Optional user path the judge call is scoped to for budgets and audit. Leave empty to use the current request's path.",
				Placeholder: "/internal/guardrails",
			},
			{
				Key: "prompt", Label: "Judge instructions", Input: pluginapi.InputTextarea, Default: DefaultPrompt,
				Help: "System prompt for the judge. It must tell the model to ignore instructions inside the content and to answer with one JSON object {\"verdict\":\"allow\"|\"block\",\"reason\":\"...\"}. The content is sent as the user message between <CONTENT> and </CONTENT> tags.",
			},
			{
				Key: "target", Label: "Prompt target", Input: pluginapi.InputSelect, Default: TargetAuto,
				Help: "What the judge sees in the prompt phase. Auto judges the last user message. In the response phase the judge always sees the assistant text of the completion.",
				Options: []pluginapi.Option{
					{Value: TargetAuto, Label: "Auto (last user message)"},
					{Value: TargetLastUser, Label: "Last user message"},
					{Value: TargetAllUser, Label: "All user messages"},
					{Value: TargetConversation, Label: "Whole conversation"},
				},
			},
			{
				Key: "action", Label: "On block verdict", Input: pluginapi.InputSelect, Default: ActionBlock,
				Help: "Block rejects with an error, respond answers with the respond text as an assistant reply, and warn continues while recording the verdict.",
				Options: []pluginapi.Option{
					{Value: ActionBlock, Label: "Block"},
					{Value: ActionRespond, Label: "Respond"},
					{Value: ActionWarn, Label: "Warn"},
				},
			},
			{
				Key: "message", Label: "Message", Input: pluginapi.InputText, Default: DefaultMessage,
				Help:        "Error message for block and audit note for warn.",
				Placeholder: DefaultMessage,
			},
			{
				Key: "block_status", Label: "Block status", Input: pluginapi.InputNumber,
				Help:        "HTTP status for block (400 to 599). Leave empty for the phase default: 400 when a prompt is blocked, 502 when a response is blocked.",
				Placeholder: "400 (prompt) / 502 (response)",
			},
			{
				Key: "respond_text", Label: "Respond text", Input: pluginapi.InputTextarea, Default: DefaultRespondText,
				Help:        "Assistant reply sent to the client when the action is respond.",
				Placeholder: DefaultRespondText,
			},
			{
				Key: "on_unclear", Label: "On unclear verdict", Input: pluginapi.InputSelect, Default: UnclearWarn,
				Help: "What to do when the judge reply contains neither a JSON verdict nor the word allow or block.",
				Options: []pluginapi.Option{
					{Value: UnclearAllow, Label: "Allow"},
					{Value: UnclearWarn, Label: "Warn"},
					{Value: UnclearBlock, Label: "Treat as block (apply the action above)"},
				},
			},
			{
				Key: "max_tokens", Label: "Max tokens", Input: pluginapi.InputNumber, Default: DefaultMaxTokens,
				Help:        "Completion cap for the judge call. The verdict is short; raise it only when the judge model spends tokens on reasoning.",
				Placeholder: fmt.Sprint(DefaultMaxTokens),
			},
			{
				Key: "temperature", Label: "Temperature", Input: pluginapi.InputNumber, Default: DefaultTemperature,
				Help:        "Sampling temperature for the judge call (0 to 2). Keep 0 for deterministic verdicts.",
				Placeholder: "0",
			},
		},
	}
}

// Init decodes and validates the instance configuration.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, host pluginapi.Host) error {
	s, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if host == nil {
		return fmt.Errorf("%s: host is required for inference", Name)
	}
	p.settings = s
	p.host = host
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
	return fmt.Sprintf("%s, %s, target %s, unclear: %s", s.model, s.action, s.target, s.onUnclear)
}
