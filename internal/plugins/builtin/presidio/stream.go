package presidio

import (
	"context"

	"github.com/enterpilot/gomodel/pluginapi"
)

// StreamPolicy transforms text in flight for anonymize and warn, in chunks
// of stream_chunk characters with stream_lookbehind of overlap, and
// buffers the whole stream for block and respond so the decision is taken
// on the assembled completion before anything reaches the client.
func (p *Plugin) StreamPolicy() pluginapi.StreamPolicy {
	switch p.action {
	case ActionBlock, ActionRespond:
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
	default:
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: p.lookbehind, MinChunkChars: p.streamChunk}
	}
}

// OnStreamEvent analyzes each text or tool-call argument window, rewrites
// it for anonymize, puts restorable values back, and cuts the stream when a
// blocking entity type appears. Reasoning windows are not analyzed: they
// only get values back. Under a buffering policy every event passes;
// OnResponse decides.
func (p *Plugin) OnStreamEvent(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if ev == nil || x == nil || ev.Text == "" {
		return pluginapi.Pass(), nil
	}
	if ev.Kind == pluginapi.EventReasoningDelta && !restoresReasoning(x) {
		return pluginapi.Pass(), nil
	}
	if p.action == ActionBlock || p.action == ActionRespond {
		return pluginapi.Pass(), nil
	}
	restore := p.restore
	var spans []span
	switch ev.Kind {
	case pluginapi.EventReasoningDelta:
		if !restore {
			return pluginapi.Pass(), nil
		}
	case pluginapi.EventToolCallDelta, pluginapi.EventTextDelta:
		if ev.Kind == pluginapi.EventToolCallDelta {
			restore = restore && p.restoresTool(ev.Tool)
		}
		var err error
		if spans, err = p.analyze(ctx, ev.Text, runeBytes(ev.Text, ev.Overlap), x.Meta.RequestID); err != nil {
			return pluginapi.StreamDecision{}, err
		}
	default:
		return pluginapi.Pass(), nil
	}
	rep := p.streamReport(x)
	m := p.mapping(x)
	m.reserve(ev.Text)
	out := p.rewriteOne(ev.Text, spans, unit{choice: ev.Choice}, grant{}, restore, m, rep, pass{json: ev.Kind == pluginapi.EventToolCallDelta, requestID: x.Meta.RequestID})
	if rep.blocked != "" {
		return pluginapi.Terminate(p.enforcement.Reject(CodeBlocked, rep.detail())), nil
	}
	if out == ev.Text {
		return pluginapi.Pass(), nil
	}
	return pluginapi.Replace(out), nil
}

// OnStreamEnd reports what the stream contained: a warning when the action
// is warn and something was found, otherwise the counts.
func (p *Plugin) OnStreamEnd(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil {
		return pluginapi.Allow(), nil
	}
	return p.decide(p.streamReport(x)), nil
}

func (p *Plugin) streamReport(x *pluginapi.Exchange) *report {
	if x.Values == nil {
		return newReport()
	}
	key := p.key + ":stream"
	if v, ok := x.Values.Get(key); ok {
		if rep, ok := v.(*report); ok {
			return rep
		}
	}
	rep := newReport()
	x.Values.Set(key, rep)
	return rep
}
