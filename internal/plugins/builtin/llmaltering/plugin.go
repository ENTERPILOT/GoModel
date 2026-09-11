package llmaltering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the plugin's manifest name.
const Name = "llm_based_altering"

const (
	maxConcurrentRewrites = 8
	wrapperStart          = "<TEXT_TO_ALTER>"
	wrapperEnd            = "</TEXT_TO_ALTER>"
)

// Plugin implements the prompt and response hooks.
type Plugin struct {
	cfg   Config
	roles map[pluginapi.Role]struct{}
	host  pluginapi.Host
}

// New returns a fresh plugin value.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Uses an auxiliary model to rewrite selected message roles before the main request reaches the provider, and assistant text on the way back.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse},
		Mutates:     true,
		Guardrail:   true,
		ConfigSchema: []pluginapi.Field{
			{
				Key:         "model",
				Label:       "Rewrite Model",
				Input:       pluginapi.InputModel,
				Required:    true,
				Help:        "Model, alias, or {provider}/{model} selector used for the auxiliary rewrite request.",
				Placeholder: "openai/gpt-4o-mini",
			},
			{
				Key:         "provider",
				Label:       "Provider",
				Input:       pluginapi.InputText,
				Help:        "Optional routing hint for the rewrite model; folded into the model selector as {provider}/{model}.",
				Placeholder: "openai",
			},
			{
				Key:      "roles",
				Label:    "Roles",
				Input:    pluginapi.InputCheckboxes,
				Required: true,
				Help:     "Choose which conversation roles should be rewritten. System includes developer messages.",
				Default:  []string{"user"},
				Options: []pluginapi.Option{
					{Value: "system", Label: "System"},
					{Value: "user", Label: "User"},
					{Value: "assistant", Label: "Assistant"},
					{Value: "tool", Label: "Tool"},
				},
			},
			{
				Key:         "max_tokens",
				Label:       "Max Tokens",
				Input:       pluginapi.InputNumber,
				Help:        "Upper bound for the auxiliary rewrite completion.",
				Placeholder: fmt.Sprintf("%d", DefaultMaxTokens),
				Default:     DefaultMaxTokens,
			},
			{
				Key:         "skip_content_prefix",
				Label:       "Skip Prefix",
				Input:       pluginapi.InputText,
				Help:        "If set, messages whose trimmed content starts with this prefix are left unchanged.",
				Placeholder: "### safe",
			},
			{
				Key:         "prompt",
				Label:       "Prompt",
				Input:       pluginapi.InputTextarea,
				Help:        "Optional custom rewrite prompt. Leave empty to use the built-in LiteLLM-derived anonymization prompt.",
				Placeholder: "Leave empty to use the built-in anonymization prompt.",
				Default:     DefaultPrompt,
			},
		},
	}
}

// Init parses the config and keeps the host for inference.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, host pluginapi.Host) error {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	p.cfg = cfg
	p.roles = make(map[pluginapi.Role]struct{}, len(cfg.Roles)+1)
	for _, role := range cfg.Roles {
		p.roles[pluginapi.Role(role)] = struct{}{}
		if role == "system" { // developer is the Responses spelling of system
			p.roles[pluginapi.RoleDeveloper] = struct{}{}
		}
	}
	p.host = host
	return nil
}

// Close releases nothing.
func (p *Plugin) Close(context.Context) error { return nil }

func (p *Plugin) rewriteText(ctx context.Context, text string) (string, error) {
	temperature := 0.0
	completion, err := p.host.Inference().Complete(ctx, pluginapi.InferenceRequest{
		Model:       p.cfg.Model,
		MaxTokens:   p.cfg.MaxTokens,
		Temperature: &temperature,
		Messages: []pluginapi.Message{
			pluginapi.TextMessage(pluginapi.RoleSystem, p.cfg.Prompt),
			pluginapi.TextMessage(pluginapi.RoleUser, wrapText(text)),
		},
	})
	if err != nil {
		return "", err
	}
	if completion == nil || len(completion.Choices) == 0 {
		return "", errors.New("llm_based_altering returned no choices")
	}
	choice := completion.Choices[0]
	if finish := strings.TrimSpace(choice.FinishReason); finish != "" && finish != "stop" {
		return "", fmt.Errorf("llm_based_altering returned non-terminal finish_reason %q", finish)
	}
	for _, part := range choice.Message.Parts {
		if part.Kind == pluginapi.PartToolCall {
			return "", errors.New("llm_based_altering returned tool calls instead of plain text")
		}
	}
	content := completion.Text(0)
	if content == "" {
		return "", errors.New("llm_based_altering returned empty content")
	}
	return unwrapText(content), nil
}

func wrapText(text string) string {
	return wrapperStart + "\n" + text + "\n" + wrapperEnd
}

func unwrapText(text string) string {
	prefix := wrapperStart + "\n"
	suffix := "\n" + wrapperEnd
	if strings.HasPrefix(text, prefix) && strings.HasSuffix(text, suffix) {
		return strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	}
	return text
}

// Normalize folds the provider hint into the stored model selector so the
// persisted config reads "provider/model" with provider cleared.
func (p *Plugin) Normalize(raw json.RawMessage) (json.RawMessage, error) {
	var values map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("invalid llm_based_altering config: %w", err)
		}
	}
	if values == nil {
		values = map[string]any{}
	}
	model, _ := values["model"].(string)
	provider, _ := values["provider"].(string)
	qualified, err := QualifiedModel(model, provider)
	if err != nil {
		return nil, err
	}
	values["model"] = qualified
	delete(values, "provider")
	return json.Marshal(values)
}

// Summarize renders the one-line summary shown in the guardrails list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return ""
	}
	var custom Config
	_ = json.Unmarshal(raw, &custom) //nolint:errcheck // ParseConfig already validated raw
	promptSummary := "default prompt"
	if strings.TrimSpace(custom.Prompt) != "" && custom.Prompt != DefaultPrompt {
		prompt := strings.Join(strings.Fields(cfg.Prompt), " ")
		const maxLen = 48
		if len(prompt) > maxLen {
			prompt = prompt[:maxLen-3] + "..."
		}
		if prompt != "" {
			promptSummary = prompt
		}
	}
	return fmt.Sprintf("%s • %s • %s", cfg.Model, strings.Join(cfg.Roles, ","), promptSummary)
}
