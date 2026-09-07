package pluginapi

import (
	"encoding/json"
	"strings"
)

// StreamMode says how GoModel drives a [StreamHook].
type StreamMode string

const (
	// StreamObserve forwards events untouched; the hook only watches. Its
	// decisions are ignored except [StreamTerminate].
	StreamObserve StreamMode = "observe"
	// StreamTransform lets the hook rewrite, drop, or terminate events in
	// flight, holding back LookbehindChars of text so a match spanning
	// events can be rewritten.
	StreamTransform StreamMode = "transform"
	// StreamBuffer collects the whole stream (up to MaxBufferBytes) and runs
	// the plugin's [ResponseHook] on the assembled completion.
	StreamBuffer StreamMode = "buffer"
)

// StreamPolicy configures how a [StreamHook] is driven.
type StreamPolicy struct {
	Mode StreamMode
	// LookbehindChars is how many trailing characters GoModel withholds in
	// transform mode so the hook can rewrite text that spans events.
	LookbehindChars int
	// MaxBufferBytes caps buffering in buffer mode; zero means the host
	// default.
	MaxBufferBytes int
}

// EventKind is the type of a parsed stream event. Unknown kinds must be
// treated like [EventOther].
type EventKind string

const (
	EventTextDelta      EventKind = "text_delta"
	EventToolCallDelta  EventKind = "tool_call_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventFinish         EventKind = "finish"
	EventUsage          EventKind = "usage"
	EventOther          EventKind = "other"
)

// StreamEvent is one parsed event of a streaming response.
type StreamEvent struct {
	// Seq is the event number, starting at 1.
	Seq  int
	Kind EventKind
	// Choice is the choice index the event belongs to.
	Choice int
	// Text is the delta text for text, tool-call argument, and reasoning
	// deltas.
	Text string
	// Overlap is the number of leading characters (runes) of Text that were
	// already presented in an earlier event of this choice: under a
	// lookbehind StreamPolicy GoModel withholds a tail of text and shows it
	// again in front of the next delta, after this plugin's earlier decision
	// was applied to it. An edit whose match ends within the first Overlap
	// characters was applied then and must not be applied again; edits that
	// extend past Overlap are new. 0 when nothing was withheld.
	Overlap int
	// Raw is the event as received. Read-only.
	Raw json.RawMessage
}

// StreamAction is what a [StreamHook] asks GoModel to do with an event.
type StreamAction string

const (
	// StreamPass forwards the event unchanged.
	StreamPass StreamAction = "pass"
	// StreamDrop suppresses the event.
	StreamDrop StreamAction = "drop"
	// StreamReplace forwards the event with StreamDecision.Text instead of
	// its own text.
	StreamReplace StreamAction = "replace"
	// StreamTerminate ends the stream with StreamDecision.Terminate.
	StreamTerminate StreamAction = "terminate"
)

// StreamDecision is the result of [StreamHook.OnStreamEvent].
type StreamDecision struct {
	Action StreamAction
	// Text is the replacement text for [StreamReplace].
	Text string
	// Terminate is the decision rendered when the stream is cut: a block
	// error or a [Respond] completion.
	Terminate *Decision
}

// Pass forwards the event unchanged.
func Pass() StreamDecision { return StreamDecision{Action: StreamPass} }

// Replace forwards the event with text instead of its own text.
func Replace(text string) StreamDecision {
	return StreamDecision{Action: StreamReplace, Text: text}
}

// Drop suppresses the event.
func Drop() StreamDecision { return StreamDecision{Action: StreamDrop} }

// Terminate ends the stream with d.
func Terminate(d Decision) StreamDecision {
	return StreamDecision{Action: StreamTerminate, Terminate: &d}
}

// StreamState accumulates what has streamed so far. The host appends events;
// hooks read it.
type StreamState struct {
	text   map[int]*strings.Builder
	events int
}

// Text returns the text streamed so far for the given choice.
func (s *StreamState) Text(choice int) string {
	if s == nil || s.text == nil {
		return ""
	}
	if b := s.text[choice]; b != nil {
		return b.String()
	}
	return ""
}

// Events returns the number of events appended so far.
func (s *StreamState) Events() int {
	if s == nil {
		return 0
	}
	return s.events
}

// Append records an event. Host-facing: only text deltas contribute to
// Text; every event counts toward Events.
func (s *StreamState) Append(ev *StreamEvent) {
	if ev == nil {
		return
	}
	s.events++
	if ev.Kind != EventTextDelta {
		return
	}
	if s.text == nil {
		s.text = map[int]*strings.Builder{}
	}
	b := s.text[ev.Choice]
	if b == nil {
		b = &strings.Builder{}
		s.text[ev.Choice] = b
	}
	b.WriteString(ev.Text)
}
