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

// OnStreamEvent analyzes each text window, rewrites it for anonymize, puts
// restorable values back, and cuts the stream when a blocking entity type
// appears. Under a buffering policy every event passes; OnResponse decides.
func (p *Plugin) OnStreamEvent(ctx context.Context, x *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	if ev == nil || ev.Kind != pluginapi.EventTextDelta || ev.Text == "" || x == nil {
		return pluginapi.Pass(), nil
	}
	if p.action == ActionBlock || p.action == ActionRespond {
		return pluginapi.Pass(), nil
	}
	rep := p.streamReport(x)
	skip := runeBytes(ev.Text, ev.Overlap)
	out, err := p.process(ctx, ev.Text, skip, unit{choice: ev.Choice}, p.mapping(x), rep, pass{restore: p.restore, requestID: x.Meta.RequestID})
	if err != nil {
		return pluginapi.StreamDecision{}, err
	}
	if rep.blocked != "" {
		return pluginapi.Terminate(p.enforce(CodeBlocked, rep.detail())), nil
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
