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
// StreamPolicy: in transform mode text deltas of a choice are coalesced
// until MinChunkChars runes are pending, the last LookbehindChars runes of
// delivered text are withheld and shown again in front of the next delta
// with Overlap set, and pass, replace, drop, and terminate are applied to
// the whole window. Reasoning deltas are presented as they arrive and may
// be replaced or dropped too. A non-text event and the end of the stream
// flush what is pending. In observe mode only terminate has an effect.
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
	d := &driver{hook: hook, x: x, policy: policy, result: &StreamResult{Text: map[int]string{}}, pending: map[int]string{}, tail: map[int]string{}}
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Kind == pluginapi.EventTextDelta {
			d.hold(ev.Choice, ev.Text)
			if policy.Mode != pluginapi.StreamTransform || policy.MinChunkChars == 0 || utf8.RuneCountInString(d.pending[ev.Choice]) >= policy.MinChunkChars {
				if err := d.flush(ctx, ev.Choice); err != nil || d.result.Terminated != nil {
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
	for _, choice := range d.order {
		if tail := d.tail[choice]; tail != "" {
			d.deliverText(choice, tail)
		}
	}
	end, err := hook.OnStreamEnd(ctx, x)
	if err != nil {
		return d.result, err
	}
	d.result.End = end
	return d.result, nil
}

type driver struct {
	hook    pluginapi.StreamHook
	x       *pluginapi.Exchange
	policy  pluginapi.StreamPolicy
	result  *StreamResult
	pending map[int]string
	tail    map[int]string
	order   []int // choices in order of first appearance
	seq     int
}

func (d *driver) hold(choice int, text string) {
	if !slices.Contains(d.order, choice) {
		d.order = append(d.order, choice)
	}
	d.pending[choice] += text
}

// flushAll presents the pending text of every choice, in order of first
// appearance, as the host does before a non-text event and at the end.
func (d *driver) flushAll(ctx context.Context) error {
	for _, choice := range d.order {
		if err := d.flush(ctx, choice); err != nil || d.result.Terminated != nil {
			return err
		}
	}
	return nil
}

// flush presents the pending text of a choice, if any.
func (d *driver) flush(ctx context.Context, choice int) error {
	text := d.pending[choice]
	if text == "" {
		return nil
	}
	d.pending[choice] = ""
	return d.present(ctx, choice, text)
}

// present shows text to the hook with the withheld tail in front of it
// and applies the decision to the whole window.
func (d *driver) present(ctx context.Context, choice int, text string) error {
	d.seq++
	overlap := utf8.RuneCountInString(d.tail[choice])
	window := d.tail[choice] + text
	ev := &pluginapi.StreamEvent{Seq: d.seq, Kind: pluginapi.EventTextDelta, Choice: choice, Text: window, Overlap: overlap}
	decision, err := d.hook.OnStreamEvent(ctx, d.x, ev)
	if err != nil {
		return err
	}
	if decision.Action == pluginapi.StreamTerminate {
		d.terminate(decision)
		return nil
	}
	out := window
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
	if d.policy.Mode == pluginapi.StreamTransform {
		keep = d.policy.LookbehindChars
	}
	cut := len(out)
	for i := 0; i < keep && cut > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(out[:cut])
		cut -= size
	}
	d.tail[choice] = out[cut:]
	if cut > 0 {
		d.deliverText(choice, out[:cut])
	}
	return nil
}

// other presents a non-text event: reasoning deltas may be replaced or
// dropped in transform mode, everything else passed or dropped.
func (d *driver) other(ctx context.Context, ev *pluginapi.StreamEvent) error {
	d.seq++
	out := *ev
	out.Seq = d.seq
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

func (d *driver) deliverText(choice int, text string) {
	d.result.Text[choice] += text
	d.result.Events = append(d.result.Events, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Choice: choice, Text: text})
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
