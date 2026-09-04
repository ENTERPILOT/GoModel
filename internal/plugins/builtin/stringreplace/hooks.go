package stringreplace

import (
	"context"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Code is the Decision.Code recorded when a rule matches and on_match is
// block, respond, or warn.
const Code = "string_replace_match"

// OnPrompt edits or inspects the prompt messages of the configured roles.
func (p *Plugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	if p.onMatch != OnMatchReplace {
		matches, messages := 0, 0
		for _, m := range x.Prompt.Messages {
			if !p.roles[m.Role] {
				continue
			}
			if n := count(p.rules, m.Text()); n > 0 {
				matches += n
				messages++
			}
		}
		return p.decide(matches, messages), nil
	}
	total, messages := 0, 0
	for i := range x.Prompt.Messages {
		m := &x.Prompt.Messages[i]
		if !p.roles[m.Role] {
			continue
		}
		n, err := p.editMessage(x.Prompt, m)
		if err != nil {
			return pluginapi.Decision{}, err
		}
		if n > 0 {
			total += n
			messages++
		}
	}
	return allowWith(replaceDetail(total, messages)), nil
}

// editMessage rewrites the text parts and tool-result text of one message
// and returns the number of replacements.
func (p *Plugin) editMessage(prompt *pluginapi.Prompt, m *pluginapi.Message) (int, error) {
	total := 0
	for j, part := range m.Parts {
		switch part.Kind {
		case pluginapi.PartText:
			out, n := apply(p.rules, part.Text)
			if n == 0 {
				continue
			}
			if err := prompt.SetText(m.ID, j, out); err != nil {
				return total, err
			}
			total += n
		case pluginapi.PartToolResult:
			if part.ToolResult == nil {
				continue
			}
			parts, n := p.editParts(part.ToolResult.Parts)
			if n == 0 {
				continue
			}
			if err := prompt.SetToolResult(m.ID, part.ToolResult.CallID, parts); err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

func (p *Plugin) editParts(parts []pluginapi.Part) ([]pluginapi.Part, int) {
	out := make([]pluginapi.Part, len(parts))
	copy(out, parts)
	total := 0
	for i := range out {
		if out[i].Kind != pluginapi.PartText {
			continue
		}
		text, n := apply(p.rules, out[i].Text)
		out[i].Text = text
		total += n
	}
	return out, total
}

// OnResponse edits or inspects the text of every completion choice.
func (p *Plugin) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	if p.onMatch != OnMatchReplace {
		matches, choices := 0, 0
		for i := range x.Response.Choices {
			if n := count(p.rules, x.Response.Text(i)); n > 0 {
				matches += n
				choices++
			}
		}
		return p.decide(matches, choices), nil
	}
	total, choices := 0, 0
	for i := range x.Response.Choices {
		changed := 0
		for j, part := range x.Response.Choices[i].Message.Parts {
			if part.Kind != pluginapi.PartText {
				continue
			}
			out, n := apply(p.rules, part.Text)
			if n == 0 {
				continue
			}
			if err := x.Response.SetText(i, j, out); err != nil {
				return pluginapi.Decision{}, err
			}
			changed += n
		}
		if changed > 0 {
			total += changed
			choices++
		}
	}
	return allowWith(replaceDetail(total, choices)), nil
}

// StreamPolicy transforms text deltas in flight for replace and warn, and
// buffers the whole stream for block and respond so the decision is taken
// on the assembled completion before anything reaches the client.
func (p *Plugin) StreamPolicy() pluginapi.StreamPolicy {
	switch p.onMatch {
	case OnMatchBlock, OnMatchRespond:
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
	default:
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: p.lookbehind}
	}
}

// OnStreamEvent rewrites text deltas for replace, remembers matches for
// warn, and passes everything else through.
func (p *Plugin) OnStreamEvent(_ context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if ev == nil || ev.Kind != pluginapi.EventTextDelta || ev.Text == "" {
		return pluginapi.Pass(), nil
	}
	switch p.onMatch {
	case OnMatchReplace:
		out, n := apply(p.rules, ev.Text)
		if n == 0 {
			return pluginapi.Pass(), nil
		}
		p.addCount(x, n)
		return pluginapi.Replace(out), nil
	case OnMatchWarn:
		if n := count(p.rules, ev.Text); n > 0 {
			p.addCount(x, n)
		}
	}
	return pluginapi.Pass(), nil
}

// OnStreamEnd reports a warning when on_match is warn and a rule matched
// during the stream; otherwise it allows.
func (p *Plugin) OnStreamEnd(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	n := p.streamCount(x)
	switch {
	case p.onMatch == OnMatchWarn && n > 0:
		return pluginapi.Warn(Code, p.message, map[string]any{"matches": n}), nil
	case p.onMatch == OnMatchReplace && n > 0:
		return allowWith(map[string]any{"replacements": n}), nil
	}
	return pluginapi.Allow(), nil
}

func (p *Plugin) addCount(x *pluginapi.Exchange, n int) {
	if x == nil || x.Values == nil {
		return
	}
	x.Values.Set(p.key+":stream_matches", p.streamCount(x)+n)
}

func (p *Plugin) streamCount(x *pluginapi.Exchange) int {
	if x == nil {
		return 0
	}
	v, _ := x.Values.Get(p.key + ":stream_matches")
	n, _ := v.(int)
	return n
}

// decide turns a match count into the block, respond, or warn decision.
func (p *Plugin) decide(matches, messages int) pluginapi.Decision {
	if matches == 0 {
		return pluginapi.Allow()
	}
	detail := map[string]any{"matches": matches, "messages": messages}
	switch p.onMatch {
	case OnMatchBlock:
		d := pluginapi.Block(p.blockStatus, Code, p.message)
		d.Detail = detail
		return d
	case OnMatchRespond:
		d := pluginapi.Respond(p.message)
		d.Code = Code
		d.Detail = detail
		return d
	default:
		return pluginapi.Warn(Code, p.message, detail)
	}
}

func replaceDetail(replacements, messages int) map[string]any {
	return map[string]any{"replacements": replacements, "messages": messages}
}

func allowWith(detail any) pluginapi.Decision {
	return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
}
