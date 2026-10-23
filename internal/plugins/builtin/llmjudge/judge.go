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
	// CodeNoVerdict marks a judge call that produced no verdict at all
	// because the completion ended first: the reply was cut off
	// (finish_reason "length") or the judge model spent it on reasoning.
	// It is recorded apart from CodeUnclear because the remedy is a higher
	// max_tokens or a non-reasoning judge model, not a different policy.
	CodeNoVerdict = "llm_judge_no_verdict"
)

// choiceSeparator joins the text of several completion choices for the
// judge.
const choiceSeparator = "\n---\n"

// OnPrompt judges the configured target of the prompt.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	return p.judge(ctx, x, p.promptInput(x.Prompt))
}

// OnResponse judges the assistant text of the completion.
func (p *Plugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	return p.judge(ctx, x, judged{text: responseContent(x.Response)})
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
func (p *Plugin) judge(ctx context.Context, x *pluginapi.Exchange, in judged) (pluginapi.Decision, error) {
	decisions := p.decisions()
	if decisions == nil {
		// A chat judge reads text only.
		in.images = nil
	}
	if strings.TrimSpace(in.text) == "" && len(in.images) == 0 {
		return p.decide(verdict{Verdict: VerdictAllow, Reason: "no text to judge"}, false), nil
	}
	key := p.key + ":" + in.hash()
	if v, ok := x.Values.Get(key); ok {
		if cached, ok := v.(verdict); ok {
			return p.decide(cached, true), nil
		}
	}
	var v verdict
	if decisions != nil {
		var err error
		if v, err = p.askDecision(ctx, decisions, in); err != nil {
			return pluginapi.Decision{}, err
		}
	} else {
		reply, err := p.ask(ctx, in.text)
		if err != nil {
			return pluginapi.Decision{}, err
		}
		v = parseVerdict(reply.text)
		// A reply the model did not finish (max_tokens reached, a filter cut
		// it) is not a verdict even when its visible part reads like one, and
		// neither is a completion the judge spent entirely on reasoning.
		if reason := reply.noVerdictReason(); reason != "" {
			v = verdict{Verdict: VerdictUnclear, Reason: reason, NoVerdict: true}
			p.logNoVerdict(reason)
		}
	}
	if x.Values != nil {
		x.Values.Set(key, v)
	}
	return p.decide(v, false), nil
}

// judgeReply is what the judge model returned: its visible text, the finish
// reason ("" when the completion has no choice), and whether the model
// emitted reasoning before it.
type judgeReply struct {
	text      string
	finish    string
	reasoning bool
}

// noVerdictReason reports why the reply carries no verdict at all, or "" when
// the reply is complete and the parser decides.
func (r judgeReply) noVerdictReason() string {
	switch {
	case r.finish != "" && r.finish != "stop":
		if r.reasoning {
			return "judge spent the completion on reasoning and was cut off before the verdict (finish_reason " + r.finish + "); raise max_tokens"
		}
		return "judge reply was cut off (finish_reason " + r.finish + ")"
	case r.reasoning && strings.TrimSpace(r.text) == "":
		return "judge returned reasoning only, with no verdict; raise max_tokens"
	}
	return ""
}

// logNoVerdict records that the judge decided nothing. The instance keeps
// running under on_unclear, so without this line an operator sees only a
// warn header on a guardrail that has stopped enforcing.
func (p *Plugin) logNoVerdict(reason string) {
	if p.host == nil {
		return
	}
	p.host.Logger().Warn(Name+": judge returned no verdict",
		"judge_model", p.model,
		"detail", reason,
		"max_tokens", p.maxTokens,
		"on_unclear", p.onUnclear,
	)
}

// ask runs the judge call and returns the judge's reply.
func (p *Plugin) ask(ctx context.Context, content string) (judgeReply, error) {
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
		return judgeReply{}, fmt.Errorf("%s: judge call failed: %w", Name, err)
	}
	if completion == nil || len(completion.Choices) == 0 {
		return judgeReply{}, nil
	}
	return judgeReply{
		text:      completion.Text(0),
		finish:    strings.TrimSpace(completion.Choices[0].FinishReason),
		reasoning: hasReasoning(completion.Choices[0]),
	}, nil
}

// hasReasoning reports whether the choice carries reasoning text.
func hasReasoning(choice pluginapi.Choice) bool {
	for _, part := range choice.Message.Parts {
		if part.Kind == pluginapi.PartReasoning && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

// wrapContent puts the content between <CONTENT> tags, neutralizing a
// closing tag inside it so the content cannot end the block early.
func wrapContent(content string) string {
	content = strings.ReplaceAll(content, "</CONTENT>", "</CONTENT_>")
	return "<CONTENT>\n" + content + "\n</CONTENT>"
}

// judged is what the judge is shown: text, plus image URLs for a decision
// model when judge_images is on.
type judged struct {
	text   string
	images []string
}

// hash keys the verdict cache of one exchange.
func (in judged) hash() string {
	h := sha256.New()
	h.Write([]byte(in.text))
	for _, image := range in.images {
		h.Write([]byte{0})
		h.Write([]byte(image))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// promptInput is what the judge sees of the prompt: the target's text, and
// the images of the same messages when judge_images is on.
func (p *Plugin) promptInput(prompt *pluginapi.Prompt) judged {
	in := judged{text: p.promptContent(prompt)}
	if !p.images {
		return in
	}
	var messages []pluginapi.Message
	switch p.target {
	case TargetAllUser:
		for _, m := range prompt.Messages {
			if m.Role == pluginapi.RoleUser {
				messages = append(messages, m)
			}
		}
	case TargetConversation:
		messages = prompt.Messages
	default:
		if m := prompt.LastUser(); m != nil {
			messages = []pluginapi.Message{*m}
		}
	}
	for _, m := range messages {
		for _, part := range m.Parts {
			if part.Kind == pluginapi.PartImage && strings.TrimSpace(part.URL) != "" {
				in.images = append(in.images, part.URL)
			}
		}
	}
	return in
}

// decide maps a verdict to the configured decision.
func (p *Plugin) decide(v verdict, cached bool) pluginapi.Decision {
	detail := map[string]any{"verdict": v.Verdict, "reason": v.Reason, "judge_model": p.model}
	if v.Probability != nil {
		detail["probability"] = *v.Probability
	}
	if cached {
		detail["cached"] = true
	}
	switch v.Verdict {
	case VerdictAllow:
		return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	case VerdictBlock:
		return p.enforcement.Enforce(Code, detail)
	}
	code, note := CodeUnclear, "judge verdict unclear"
	if v.NoVerdict {
		code, note = CodeNoVerdict, "judge returned no verdict"
	}
	switch p.onUnclear {
	case UnclearAllow:
		return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	case UnclearBlock:
		return p.enforcement.Enforce(code, detail)
	default:
		return pluginapi.Warn(code, note, detail)
	}
}
