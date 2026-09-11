package plugintest

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
)

// StreamResult is what a client would have received from a stream driven
// through a hook.
type StreamResult struct {
	// Text is the delivered text per choice, after the hook's edits.
	Text map[int]string
	// Events are the delivered events in order, text deltas carrying the
	// text as delivered.
	Events []*pluginapi.StreamEvent
	// Terminated is the decision the hook cut the stream with, or nil.
	Terminated *pluginapi.Decision
	// End is the OnStreamEnd decision (or, under a buffering policy, the
	// OnResponse decision on the assembled completion). Zero when the
	// stream was terminated.
	End pluginapi.Decision
	// Response is the assembled completion under a buffering policy, after
	// OnResponse edited it; nil otherwise.
	Response *pluginapi.Completion
}

// RunStream drives hook with events the way GoModel does under its
// StreamPolicy: in transform mode text deltas of a choice are coalesced
// until MinChunkChars runes are pending, the last LookbehindChars runes of
// delivered text are withheld and shown again in front of the next delta
// with Overlap set, and pass, replace, drop, and terminate are applied to
// the whole window. A non-text event and the end of the stream flush what
// is pending. In observe mode only terminate has an effect. In buffer
// mode nothing is presented per event: the deltas are assembled into a
// completion in x.Response and the plugin's ResponseHook decides.
//
// Only choice and text matter on the input events; Seq and Overlap are set
// by the driver.
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
		if ev.Kind == pluginapi.EventTextDelta && policy.Mode == pluginapi.StreamTransform {
			d.pending[ev.Choice] += ev.Text
			if policy.MinChunkChars > 0 && utf8.RuneCountInString(d.pending[ev.Choice]) < policy.MinChunkChars {
				continue
			}
			if err := d.flush(ctx, ev.Choice); err != nil || d.result.Terminated != nil {
				return d.result, err
			}
			continue
		}
		if ev.Kind == pluginapi.EventTextDelta {
			if err := d.present(ctx, ev.Choice, ev.Text, 0); err != nil || d.result.Terminated != nil {
				return d.result, err
			}
			continue
		}
		if err := d.flush(ctx, ev.Choice); err != nil || d.result.Terminated != nil {
			return d.result, err
		}
		if err := d.other(ctx, ev); err != nil || d.result.Terminated != nil {
			return d.result, err
		}
	}
	for choice := range d.pending {
		if err := d.flush(ctx, choice); err != nil || d.result.Terminated != nil {
			return d.result, err
		}
	}
	for choice, tail := range d.tail {
		if tail != "" {
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
	seq     int
}

// flush presents the pending text of a choice, if any.
func (d *driver) flush(ctx context.Context, choice int) error {
	text := d.pending[choice]
	if text == "" {
		return nil
	}
	d.pending[choice] = ""
	return d.present(ctx, choice, text, utf8.RuneCountInString(d.tail[choice]))
}

// present shows text to the hook with the withheld tail in front of it
// and applies the decision to the whole window.
func (d *driver) present(ctx context.Context, choice int, text string, overlap int) error {
	d.seq++
	window := d.tail[choice] + text
	ev := &pluginapi.StreamEvent{Seq: d.seq, Kind: pluginapi.EventTextDelta, Choice: choice, Text: window, Overlap: overlap}
	decision, err := d.hook.OnStreamEvent(ctx, d.x, ev)
	if err != nil {
		return err
	}
	out := window
	if d.policy.Mode == pluginapi.StreamTransform {
		switch decision.Action {
		case pluginapi.StreamReplace:
			out = decision.Text
		case pluginapi.StreamDrop:
			out = ""
		case pluginapi.StreamTerminate:
			d.terminate(decision)
			return nil
		}
	} else if decision.Action == pluginapi.StreamTerminate {
		d.terminate(decision)
		return nil
	}
	d.x.Stream.ReplaceTail(ev, overlap, out)
	keep := 0
	if d.policy.Mode == pluginapi.StreamTransform && d.policy.LookbehindChars > 0 {
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

func (d *driver) other(ctx context.Context, ev *pluginapi.StreamEvent) error {
	d.seq++
	copy := *ev
	copy.Seq = d.seq
	decision, err := d.hook.OnStreamEvent(ctx, d.x, &copy)
	if err != nil {
		return err
	}
	d.x.Stream.Append(&copy)
	switch {
	case decision.Action == pluginapi.StreamTerminate:
		d.terminate(decision)
	case decision.Action == pluginapi.StreamDrop && d.policy.Mode == pluginapi.StreamTransform:
	case decision.Action == pluginapi.StreamReplace && d.policy.Mode == pluginapi.StreamTransform:
		return fmt.Errorf("plugintest: replace on event %d (%s): only text and reasoning deltas can be replaced", copy.Seq, copy.Kind)
	default:
		d.result.Events = append(d.result.Events, &copy)
	}
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

// runBuffered assembles the deltas into x.Response and runs the plugin's
// ResponseHook on it, as the host does for a buffering policy.
func runBuffered(ctx context.Context, hook pluginapi.StreamHook, x *pluginapi.Exchange, events []*pluginapi.StreamEvent) (*StreamResult, error) {
	result := &StreamResult{Text: map[int]string{}}
	texts := map[int]*strings.Builder{}
	maxChoice := -1
	for _, ev := range events {
		if ev == nil {
			continue
		}
		x.Stream.Append(ev)
		if ev.Choice > maxChoice {
			maxChoice = ev.Choice
		}
		if ev.Kind == pluginapi.EventTextDelta {
			if texts[ev.Choice] == nil {
				texts[ev.Choice] = &strings.Builder{}
			}
			texts[ev.Choice].WriteString(ev.Text)
		}
	}
	c := &pluginapi.Completion{}
	for i := 0; i <= maxChoice; i++ {
		text := ""
		if b := texts[i]; b != nil {
			text = b.String()
		}
		c.Choices = append(c.Choices, pluginapi.Choice{Index: i, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, text), FinishReason: "stop"})
	}
	x.Response = c
	result.Response = c
	responder, ok := hook.(pluginapi.ResponseHook)
	if !ok {
		return nil, fmt.Errorf("plugintest: a buffering stream plugin must implement pluginapi.ResponseHook")
	}
	d, err := responder.OnResponse(ctx, x)
	if err != nil {
		return result, err
	}
	result.End = d
	if !d.Blocks() {
		for i := range c.Choices {
			result.Text[i] = c.Text(i)
		}
	}
	return result, nil
}
