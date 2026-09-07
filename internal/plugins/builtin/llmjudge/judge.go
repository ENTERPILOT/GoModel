package llmjudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Decision codes recorded in the audit trail.
const (
	// Code marks a block verdict.
	Code = "llm_judge_block"
	// CodeUnclear marks a judge reply that could not be parsed.
	CodeUnclear = "llm_judge_unclear"
)

// choiceSeparator joins the text of several completion choices for the
// judge.
const choiceSeparator = "\n---\n"

// OnPrompt judges the configured target of the prompt.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	return p.judge(ctx, x, p.promptContent(x.Prompt))
}

// OnResponse judges the assistant text of the completion.
func (p *Plugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	return p.judge(ctx, x, responseContent(x.Response))
}

// StreamPolicy buffers the stream so the host runs OnResponse on the
// assembled completion before anything reaches the client.
func (p *Plugin) StreamPolicy() pluginapi.StreamPolicy {
	return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
}

// OnStreamEvent passes every event; the verdict is taken in OnResponse.
func (p *Plugin) OnStreamEvent(context.Context, *pluginapi.Exchange, *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	return pluginapi.Pass(), nil
}

// OnStreamEnd allows; the verdict is taken in OnResponse.
func (p *Plugin) OnStreamEnd(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func (p *Plugin) promptContent(prompt *pluginapi.Prompt) string {
	switch p.target {
	case TargetAllUser:
		return prompt.Text(pluginapi.RoleUser)
	case TargetConversation:
		var lines []string
		for _, m := range prompt.Messages {
			if text := m.Text(); text != "" {
				lines = append(lines, string(m.Role)+": "+text)
			}
		}
		return strings.Join(lines, "\n\n")
	default:
		if m := prompt.LastUser(); m != nil {
			return m.Text()
		}
		return ""
	}
}

func responseContent(c *pluginapi.Completion) string {
	var texts []string
	for i := range c.Choices {
		if text := c.Text(i); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, choiceSeparator)
}

// judge asks the model about content, remembering the verdict in
// Exchange.Values so identical text is judged once per request.
func (p *Plugin) judge(ctx context.Context, x *pluginapi.Exchange, content string) (pluginapi.Decision, error) {
	if strings.TrimSpace(content) == "" {
		return p.decide(verdict{Verdict: VerdictAllow, Reason: "no text to judge"}, false), nil
	}
	key := p.key + ":" + contentHash(content)
	if v, ok := x.Values.Get(key); ok {
		if cached, ok := v.(verdict); ok {
			return p.decide(cached, true), nil
		}
	}
	reply, err := p.ask(ctx, content)
	if err != nil {
		return pluginapi.Decision{}, err
	}
	v := parseVerdict(reply)
	if x.Values != nil {
		x.Values.Set(key, v)
	}
	return p.decide(v, false), nil
}

func (p *Plugin) ask(ctx context.Context, content string) (string, error) {
	temperature := p.temperature
	completion, err := p.host.Inference().Complete(ctx, pluginapi.InferenceRequest{
		Model:    p.model,
		UserPath: p.userPath,
		Messages: []pluginapi.Message{
			pluginapi.TextMessage(pluginapi.RoleSystem, p.prompt),
			pluginapi.TextMessage(pluginapi.RoleUser, wrapContent(content)),
		},
		MaxTokens:   p.maxTokens,
		Temperature: &temperature,
	})
	if err != nil {
		return "", fmt.Errorf("%s: judge call failed: %w", Name, err)
	}
	if completion == nil {
		return "", nil
	}
	return completion.Text(0), nil
}

// wrapContent puts the content between <CONTENT> tags, neutralizing a
// closing tag inside it so the content cannot end the block early.
func wrapContent(content string) string {
	content = strings.ReplaceAll(content, "</CONTENT>", "</CONTENT_>")
	return "<CONTENT>\n" + content + "\n</CONTENT>"
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// decide maps a verdict to the configured decision.
func (p *Plugin) decide(v verdict, cached bool) pluginapi.Decision {
	detail := map[string]any{"verdict": v.Verdict, "reason": v.Reason, "judge_model": p.model}
	if cached {
		detail["cached"] = true
	}
	switch v.Verdict {
	case VerdictAllow:
		return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	case VerdictBlock:
		return p.enforce(Code, detail)
	}
	switch p.onUnclear {
	case UnclearAllow:
		return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	case UnclearBlock:
		return p.enforce(CodeUnclear, detail)
	default:
		return pluginapi.Warn(CodeUnclear, "judge verdict unclear", detail)
	}
}

// enforce renders a block verdict as the configured action.
func (p *Plugin) enforce(code string, detail map[string]any) pluginapi.Decision {
	switch p.action {
	case ActionRespond:
		d := pluginapi.Respond(p.respondText)
		d.Code = code
		d.Detail = detail
		return d
	case ActionWarn:
		return pluginapi.Warn(code, p.message, detail)
	default:
		d := pluginapi.Block(p.blockStatus, code, p.message)
		d.Detail = detail
		return d
	}
}
