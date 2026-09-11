package plugintest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
)

// StreamResult is what a client would have received from a stream driven
// through a hook.
type StreamResult struct {
	// Text is the delivered text per choice, after the hook's edits.
	Text map[int]string
	// ToolArguments is the delivered tool-call arguments per choice and
	// call index, after the hook's edits.
	ToolArguments map[int]map[int]string
	// Events are the delivered events in order, text and reasoning deltas
	// carrying the text as delivered.
	Events []*pluginapi.StreamEvent
	// Terminated is the decision the hook cut the stream with, or nil.
	Terminated *pluginapi.Decision
	// End is the OnStreamEnd decision (or, under a buffering policy, the
	// OnResponse decision on the assembled completion). Zero when the
	// stream was terminated.
	End pluginapi.Decision
	// Response is the completion the ResponseHook saw under a buffering
	// policy, after its edits; nil otherwise.
	Response *pluginapi.Completion
}

// RunStream drives hook with events the way GoModel does under its
// StreamPolicy: in transform mode the text deltas of a choice, and the
// argument deltas of each of its tool calls, form windows that are
// coalesced until MinChunkChars runes are pending; the last LookbehindChars
// runes of a delivered window are withheld and shown again in front of the
// next delta with Overlap set, and pass, replace, drop, and terminate are
// applied to the whole window. Reasoning deltas are presented as they
// arrive and may be replaced or dropped too. A delta for another window of
// the same choice flushes that choice's other windows, an event that is
// not held flushes every window, and so does the end of the stream. In
// observe mode only terminate has an effect.
//
// In buffer mode nothing is presented per event: the deltas are assembled
// into a completion (text and reasoning parts, "stop" as finish reason)
// and the plugin's ResponseHook decides. Set x.Response beforehand to hand
// the hook a completion of your own, with tool calls, usage, or another
// finish reason.
//
// Only choice, kind, and text matter on the input events; Seq and Overlap
// are set by the driver.
func RunStream(ctx context.Context, hook pluginapi.StreamHook, x *pluginapi.Exchange, events []*pluginapi.StreamEvent) (*StreamResult, error) {
	if x == nil {
		x = Exchange(nil, nil)
	}
	if x.Stream == nil {
		x.Stream = &pluginapi.StreamState{}
	}
	if x.Values == nil {
		x.Values = pluginapi.Values{}
	}
	policy := hook.StreamPolicy()
	if policy.Mode == pluginapi.StreamBuffer {
		return runBuffered(ctx, hook, x, events)
	}
	d := &driver{hook: hook, x: x, policy: policy, result: &StreamResult{Text: map[int]string{}, ToolArguments: map[int]map[int]string{}}, pending: map[window]string{}, tail: map[window]string{}}
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Kind == pluginapi.EventTextDelta || (ev.Kind == pluginapi.EventToolCallDelta && ev.Text != "") {
			w := windowOf(ev)
			if err := d.flushOthers(ctx, w); err != nil || d.result.Terminated != nil {
				return d.result, err
			}
			d.hold(w, ev.Text)
			if policy.Mode != pluginapi.StreamTransform || policy.MinChunkChars == 0 || utf8.RuneCountInString(d.pending[w]) >= policy.MinChunkChars {
				if err := d.present(ctx, w, false); err != nil || d.result.Terminated != nil {
					return d.result, err
				}
			}
			continue
		}
		if err := d.flushAll(ctx); err != nil || d.result.Terminated != nil {
			return d.result, err
		}
		if err := d.other(ctx, ev); err != nil || d.result.Terminated != nil {
			return d.result, err
		}
	}
	if err := d.flushAll(ctx); err != nil || d.result.Terminated != nil {
		return d.result, err
	}
	end, err := hook.OnStreamEnd(ctx, x)
	if err != nil {
		return d.result, err
	}
	d.result.End = end
	return d.result, nil
}

// window identifies a choice's text or one of its tool calls' arguments.
type window struct {
	choice int
	call   int
	kind   pluginapi.EventKind
}

func windowOf(ev *pluginapi.StreamEvent) window {
	w := window{choice: ev.Choice, kind: ev.Kind}
	if ev.Kind == pluginapi.EventToolCallDelta {
		w.call = ev.Call
	}
	return w
}

type driver struct {
	hook    pluginapi.StreamHook
	x       *pluginapi.Exchange
	policy  pluginapi.StreamPolicy
	result  *StreamResult
	pending map[window]string
	tail    map[window]string
	order   []window // windows in order of first appearance
	seq     int
}

func (d *driver) hold(w window, text string) {
	if !slices.Contains(d.order, w) {
		d.order = append(d.order, w)
	}
	d.pending[w] += text
}

// flushAll shows the transformer every window's tail once more with its
// pending text and delivers the results in full, as the host does before
// an event that is not held and at the end.
func (d *driver) flushAll(ctx context.Context) error {
	for _, w := range d.order {
		if err := d.present(ctx, w, true); err != nil || d.result.Terminated != nil {
			return err
		}
	}
	return nil
}

// flushOthers flushes the other windows of w's choice, as the host does
// when a delta for another window arrives.
func (d *driver) flushOthers(ctx context.Context, w window) error {
	for _, other := range d.order {
		if other.choice != w.choice || other == w {
			continue
		}
		if err := d.present(ctx, other, true); err != nil || d.result.Terminated != nil {
			return err
		}
	}
	return nil
}

// present shows the window (the withheld tail followed by the pending
// text) to the hook and applies the decision to all of it. A final flush
// delivers the result in full; otherwise the last LookbehindChars runes
// are withheld as the new tail.
func (d *driver) present(ctx context.Context, w window, final bool) error {
	full := d.tail[w] + d.pending[w]
	if full == "" {
		return nil
	}
	overlap := utf8.RuneCountInString(d.tail[w])
	d.tail[w], d.pending[w] = "", ""
	d.seq++
	ev := &pluginapi.StreamEvent{Seq: d.seq, Kind: w.kind, Choice: w.choice, Call: w.call, Text: full, Overlap: overlap}
	decision, err := d.hook.OnStreamEvent(ctx, d.x, ev)
	if err != nil {
		return err
	}
	if decision.Action == pluginapi.StreamTerminate {
		d.terminate(decision)
		return nil
	}
	out := full
	if d.policy.Mode == pluginapi.StreamTransform {
		switch decision.Action {
		case pluginapi.StreamReplace:
			out = decision.Text
		case pluginapi.StreamDrop:
			out = ""
		}
	}
	d.x.Stream.ReplaceTail(ev, overlap, out)
	keep := 0
	if d.policy.Mode == pluginapi.StreamTransform && !final {
		keep = d.policy.LookbehindChars
	}
	cut := len(out)
	for i := 0; i < keep && cut > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(out[:cut])
		cut -= size
	}
	d.tail[w] = out[cut:]
	if cut > 0 {
		d.deliver(w, out[:cut])
	}
	return nil
}

// other presents a non-text event: reasoning deltas may be replaced or
// dropped in transform mode, everything else passed or dropped.
func (d *driver) other(ctx context.Context, ev *pluginapi.StreamEvent) error {
	d.seq++
	out := *ev
	out.Seq = d.seq
	out.Overlap = 0 // only re-segmented text deltas carry an overlap
	decision, err := d.hook.OnStreamEvent(ctx, d.x, &out)
	if err != nil {
		return err
	}
	if decision.Action == pluginapi.StreamTerminate {
		d.terminate(decision)
		return nil
	}
	if d.policy.Mode == pluginapi.StreamTransform {
		switch decision.Action {
		case pluginapi.StreamDrop:
			return nil
		case pluginapi.StreamReplace:
			if out.Kind != pluginapi.EventReasoningDelta {
				return fmt.Errorf("plugintest: replace on event %d (%s): only text and reasoning deltas can be replaced", out.Seq, out.Kind)
			}
			out.Text = decision.Text
		}
	}
	d.x.Stream.Append(&out)
	d.result.Events = append(d.result.Events, &out)
	return nil
}

func (d *driver) deliver(w window, text string) {
	if w.kind == pluginapi.EventToolCallDelta {
		if d.result.ToolArguments[w.choice] == nil {
			d.result.ToolArguments[w.choice] = map[int]string{}
		}
		d.result.ToolArguments[w.choice][w.call] += text
	} else {
		d.result.Text[w.choice] += text
	}
	d.result.Events = append(d.result.Events, &pluginapi.StreamEvent{Kind: w.kind, Choice: w.choice, Call: w.call, Text: text})
}

func (d *driver) terminate(decision pluginapi.StreamDecision) {
	t := decision.Terminate
	if t == nil {
		t = &pluginapi.Decision{Action: pluginapi.ActionBlock}
	}
	d.result.Terminated = t
}

// runBuffered assembles the deltas into x.Response (unless one is set) and
// runs the plugin's ResponseHook on it, as the host does for a buffering
// policy.
func runBuffered(ctx context.Context, hook pluginapi.StreamHook, x *pluginapi.Exchange, events []*pluginapi.StreamEvent) (*StreamResult, error) {
	result := &StreamResult{Text: map[int]string{}}
	responder, ok := hook.(pluginapi.ResponseHook)
	if !ok {
		return nil, fmt.Errorf("plugintest: a buffering stream plugin must implement pluginapi.ResponseHook")
	}
	for _, ev := range events {
		if ev != nil {
			x.Stream.Append(ev)
		}
	}
	if x.Response == nil {
		x.Response = assemble(events)
	}
	result.Response = x.Response
	d, err := responder.OnResponse(ctx, x)
	if err != nil {
		return result, err
	}
	result.End = d
	delivered := x.Response
	switch d.Action {
	case pluginapi.ActionRespond:
		delivered = d.Response
	case pluginapi.ActionBlock:
		return result, nil
	}
	for i := range delivered.Choices {
		result.Text[i] = delivered.Text(i)
	}
	return result, nil
}

// assemble builds a completion from the deltas: per choice, one reasoning
// part when reasoning streamed and one text part, finished with "stop".
func assemble(events []*pluginapi.StreamEvent) *pluginapi.Completion {
	texts, reasoning := map[int]*strings.Builder{}, map[int]*strings.Builder{}
	maxChoice := -1
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Choice > maxChoice {
			maxChoice = ev.Choice
		}
		switch ev.Kind {
		case pluginapi.EventTextDelta:
			build(texts, ev.Choice).WriteString(ev.Text)
		case pluginapi.EventReasoningDelta:
			build(reasoning, ev.Choice).WriteString(ev.Text)
		}
	}
	c := &pluginapi.Completion{}
	for i := 0; i <= maxChoice; i++ {
		var parts []pluginapi.Part
		if b := reasoning[i]; b != nil {
			parts = append(parts, pluginapi.Part{Kind: pluginapi.PartReasoning, Text: b.String()})
		}
		text := ""
		if b := texts[i]; b != nil {
			text = b.String()
		}
		parts = append(parts, pluginapi.Part{Kind: pluginapi.PartText, Text: text})
		c.Choices = append(c.Choices, pluginapi.Choice{Index: i, Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: parts}, FinishReason: "stop"})
	}
	return c
}

func build(m map[int]*strings.Builder, choice int) *strings.Builder {
	if m[choice] == nil {
		m[choice] = &strings.Builder{}
	}
	return m[choice]
}
