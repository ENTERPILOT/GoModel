package pluginapi

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// StreamMode says how GoModel drives a [StreamHook].
type StreamMode string

const (
	// StreamObserve forwards events untouched; the hook only watches. Its
	// decisions are ignored except [StreamTerminate].
	StreamObserve StreamMode = "observe"
	// StreamTransform lets the hook rewrite, drop, or terminate events in
	// flight, holding back LookbehindChars of text (and of tool-call
	// arguments, per call) so a match spanning events can be rewritten.
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
	// MinChunkChars, in transform mode, makes GoModel collect the text
	// deltas of a choice (and, per call, its tool-call argument deltas)
	// until at least this many new characters (runes)
	// are pending and present them to the hook as one text event, so a
	// hook whose per-call cost is high (a classifier, a named-entity
	// detector) runs on windows of useful size instead of on every token.
	// A non-text event and the end of the stream flush what is pending
	// early. Text reaches the client only after the hook saw it, so the
	// client waits for up to MinChunkChars characters of text at a time.
	// The largest value among the in-flight instances of a stream applies
	// to all of them, capped by the host at 16384 characters. Zero presents
	// deltas as they arrive.
	MinChunkChars int
	// MaxBufferBytes caps buffering in buffer mode; zero means the host
	// default. The buffer is shared by every plugin buffering the same
	// stream, so the largest cap asked for applies, and the host default
	// when any of them asks for none.
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
	// Call is the index of the tool call a tool-call delta belongs to
	// within its choice; 0 for other kinds. Each tool call's arguments are
	// a window of their own under lookbehind and coalescing.
	Call int
	// Text is the delta text for text, tool-call argument, and reasoning
	// deltas.
	Text string
	// Overlap is the number of leading characters (runes) of Text that were
	// already presented in an earlier event of this window (a choice's
	// text, or the arguments of one of its tool calls): under a lookbehind
	// StreamPolicy GoModel withholds a tail of text and shows it again in
	// front of the next delta, after this plugin's earlier decision was
	// applied to it. An edit whose match ends within the first Overlap
	// characters was applied then and must not be applied again; edits that
	// extend past Overlap are new. 0 when nothing was withheld.
	Overlap int
	// Final marks the last event of a window (the stream ended, or a delta
	// of another kind closed it): nothing is withheld after it, so an edit
	// a plugin put off because the text could still grow is due now.
	Final bool
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
	// its own text: a text, reasoning, or tool-call argument delta.
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

// StreamState accumulates what has streamed so far, as the client receives
// it: text a transform hook replaced or dropped is recorded that way, so a
// hook reading it in [StreamHook.OnStreamEnd] sees the delivered text. The
// host appends events; hooks read it.
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
	s.builder(ev.Choice).WriteString(ev.Text)
}

// ReplaceTail records ev as delivered with text in place of its window: the
// last tail runes recorded for the choice (the withheld text shown again in
// front of ev under lookbehind, plus any of ev's own text already recorded)
// are removed and text is appended. An empty text records a dropped window.
// Host-facing: only text deltas change Text; every event counts toward
// Events.
func (s *StreamState) ReplaceTail(ev *StreamEvent, tail int, text string) {
	if ev == nil {
		return
	}
	s.events++
	if ev.Kind != EventTextDelta {
		return
	}
	b := s.builder(ev.Choice)
	current := b.String()
	cut := len(current)
	for i := 0; i < tail && cut > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(current[:cut])
		cut -= size
	}
	b.Reset()
	b.WriteString(current[:cut])
	b.WriteString(text)
}

func (s *StreamState) builder(choice int) *strings.Builder {
	if s.text == nil {
		s.text = map[int]*strings.Builder{}
	}
	b := s.text[choice]
	if b == nil {
		b = &strings.Builder{}
		s.text[choice] = b
	}
	return b
}
