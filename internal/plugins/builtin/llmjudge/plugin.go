// Package llmjudge is the built-in llm_judge plugin: it asks a second model
// whether a prompt or a completion violates a policy and blocks, answers,
// or flags the exchange based on the verdict. A chat model is asked for a
// JSON verdict; a decision model (Jev, Kev, OpenRouter's decision models,
// OpenAI's Decisions API) is asked a yes-or-no question and blocks on the
// probability of yes. It doubles as a reference implementation of a
// pluginapi plugin that uses Host.Inference and the optional
// DecisionsHost, and buffers streams.
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
		Description: "Asks a judge model, a chat model or a decision model, whether prompts and responses violate a policy, then blocks, answers, or flags them.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates:     false,
		Guardrail:   true,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "model", Label: "Judge model", Input: pluginapi.InputModel, Required: true,
				Help:        "Model that judges the content: a \"provider/model\" reference, an alias, or a virtual model. A chat model or a decision model such as jev/jev-latest; see Engine. Its usage is accounted to this gateway with origin \"plugin\".",
				Placeholder: "openai/gpt-4o-mini",
			},
			{
				Key: "engine", Label: "Engine", Input: pluginapi.InputSelect, Default: EngineAuto,
				Help: "How the judge model is asked. Chat sends the judge instructions and parses a JSON verdict. Decision asks the decision question through the Decisions API and blocks on the probability of yes: cheaper, and a calibrated score rather than text to parse. Auto uses decision for a model that answers decisions only (Jev, Kev, OpenRouter's decision models, or a virtual model whose targets all are) and chat otherwise; choose decision explicitly for an OpenAI model with Decisions support.",
				Options: []pluginapi.Option{
					{Value: EngineAuto, Label: "Auto"},
					{Value: EngineChat, Label: "Chat"},
					{Value: EngineDecision, Label: "Decision"},
				},
			},
			{
				Key: "user_path", Label: "User path", Input: pluginapi.InputText, Default: "",
				Help:        "Optional user path the judge call is scoped to for budgets and audit. Leave empty to use the current request's path.",
				Placeholder: "/internal/guardrails",
			},
			{
				Key: "prompt", Label: "Judge instructions", Input: pluginapi.InputTextarea, Default: DefaultPrompt,
				Help: "Chat engine only. System prompt for the judge. It must tell the model to ignore instructions inside the content and to answer with one JSON object {\"verdict\":\"allow\"|\"block\",\"reason\":\"...\"}. The content is sent as the user message between <CONTENT> and </CONTENT> tags.",
			},
			{
				Key: "decision_question", Label: "Decision question", Input: pluginapi.InputTextarea, Default: DefaultDecisionQuestion,
				Help: "Decision engine only. Yes-or-no question asked about the content; yes means the content violates the policy. The content is sent as the input, without tags.",
			},
			{
				Key: "block_threshold", Label: "Block threshold", Input: pluginapi.InputNumber, Default: DefaultBlockThreshold,
				Help:        "Decision engine only. Probability of yes, from 0 to 1, at or above which the content is treated as a block verdict. Raise it to block only clear violations, lower it to block more borderline content. The probability is recorded with each verdict.",
				Placeholder: "0.5",
			},
			{
				Key: "judge_images", Label: "Judge images", Input: pluginapi.InputBool, Default: false,
				Help: "Decision engine only. Also send the images of the judged messages. Leave off for a judge that reads text only, such as a Kev server: with images it fails each call, and the fail mode decides. Hosted Jev reads images sent as base64 data URLs.",
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
			pluginapi.BlockStatusField(),
			{
				Key: "respond_text", Label: "Respond text", Input: pluginapi.InputTextarea, Default: DefaultRespondText,
				Help:        "Assistant reply sent to the client when the action is respond.",
				Placeholder: DefaultRespondText,
			},
			{
				Key: "on_unclear", Label: "On unclear verdict", Input: pluginapi.InputSelect, Default: UnclearWarn,
				Help: "What to do when the judge reply is neither a JSON verdict nor a bare allow or block, was cut off before it finished, or was spent entirely on reasoning. Allow and warn let the request through: choose block for a policy the instance must enforce even when the judge answers nothing usable. A judge call that fails outright follows the instance's fail mode instead.",
				Options: []pluginapi.Option{
					{Value: UnclearAllow, Label: "Allow"},
					{Value: UnclearWarn, Label: "Warn"},
					{Value: UnclearBlock, Label: "Treat as block (apply the action above)"},
				},
			},
			{
				Key: "max_tokens", Label: "Max tokens", Input: pluginapi.InputNumber, Default: DefaultMaxTokens,
				Help:        "Chat engine only. Completion cap for the judge call; unused tokens cost nothing. The verdict itself is short, but a reasoning judge model spends the cap on thinking first: when it runs out before the verdict the call is recorded as llm_judge_no_verdict and on_unclear decides. Raise it for a judge that reasons a lot.",
				Placeholder: fmt.Sprint(DefaultMaxTokens),
			},
			{
				Key: "temperature", Label: "Temperature", Input: pluginapi.InputNumber, Default: DefaultTemperature,
				Help:        "Chat engine only. Sampling temperature for the judge call (0 to 2). Keep 0 for deterministic verdicts.",
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
	if _, ok := host.(pluginapi.DecisionsHost); s.engine == EngineDecision && !ok {
		return fmt.Errorf("%s: engine decision needs a host that supports decisions", Name)
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
	summary := fmt.Sprintf("%s, %s, target %s, unclear: %s", s.model, s.enforcement.Action, s.target, s.onUnclear)
	if s.engine != EngineAuto {
		summary += ", engine " + s.engine
	}
	return summary
}
