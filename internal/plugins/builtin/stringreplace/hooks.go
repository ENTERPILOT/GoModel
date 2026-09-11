package stringreplace

import (
	"context"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Code is the Decision.Code recorded when a rule matches and on_match is
// block, respond, or warn.
const Code = "string_replace_match"

// OnPrompt edits or inspects the text of the prompt messages of the
// configured roles, tool-result text included.
func (p *Plugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	var targets []pluginapi.TextTarget
	for _, t := range x.Prompt.TextTargets() {
		if p.roles[t.Role] {
			targets = append(targets, t)
		}
	}
	if p.onMatch != OnMatchReplace {
		matches, messages, _ := p.scan(targets, nil)
		return p.decide(matches, messages), nil
	}
	total, messages, err := p.scan(targets, x.Prompt.SetTargetText)
	if err != nil {
		return pluginapi.Decision{}, err
	}
	return allowWith(replaceDetail(total, messages)), nil
}

// OnResponse edits or inspects the text of every completion choice.
func (p *Plugin) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	targets := x.Response.TextTargets()
	if p.onMatch != OnMatchReplace {
		matches, choices, _ := p.scan(targets, nil)
		return p.decide(matches, choices), nil
	}
	total, choices, err := p.scan(targets, x.Response.SetTargetText)
	if err != nil {
		return pluginapi.Decision{}, err
	}
	return allowWith(replaceDetail(total, choices)), nil
}

// unit identifies the message or choice a target belongs to.
type unit struct {
	message string
	choice  int
}

// scan applies the rules to each target on its own and returns the number of
// matches and the number of messages (or choices) with at least one match.
// With a nil set the text is only counted; otherwise every rewritten target
// is written back through set. Block, respond and warn therefore agree with
// replace on what is a match: text split across two parts is matched in no
// mode.
func (p *Plugin) scan(targets []pluginapi.TextTarget, set func(pluginapi.TextTarget, string) error) (int, int, error) {
	total := 0
	units := map[unit]bool{}
	for _, t := range targets {
		if set == nil {
			n := count(p.rules, t.Text, 0)
			if n > 0 {
				total += n
				units[unit{t.MessageID, t.Choice}] = true
			}
			continue
		}
		out, n := apply(p.rules, t.Text, 0)
		if n == 0 {
			continue
		}
		if err := set(t, out); err != nil {
			return total, len(units), err
		}
		total += n
		units[unit{t.MessageID, t.Choice}] = true
	}
	return total, len(units), nil
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
	skip := overlapBytes(ev)
	switch p.onMatch {
	case OnMatchReplace:
		out, n := apply(p.rules, ev.Text, skip)
		if n == 0 {
			return pluginapi.Pass(), nil
		}
		p.addCount(x, n)
		return pluginapi.Replace(out), nil
	case OnMatchWarn:
		if n := count(p.rules, ev.Text, skip); n > 0 {
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
