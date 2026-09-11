package streaming

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Action is what a Transformer wants done with an event.
type Action string

const (
	ActionPass      Action = "pass"
	ActionDrop      Action = "drop"
	ActionReplace   Action = "replace"
	ActionTerminate Action = "terminate"
)

// Decision is a Transformer's verdict on one event.
type Decision struct {
	Action Action
	// Text is the replacement delta text for ActionReplace. Text,
	// reasoning, and tool-call argument deltas can be replaced.
	Text string
	// Terminate describes how to end the stream for ActionTerminate; nil
	// selects the codec defaults (finish_reason "content_filter").
	Terminate *Termination
}

// Transformer inspects and edits a stream event by event.
type Transformer interface {
	// OnEvent is called for every decoded event except the [DONE] sentinel.
	// The event, including Data, is only valid during the call.
	OnEvent(ev *Event) (Decision, error)
	// OnEnd is called once after the last upstream event and before [DONE]
	// (or at upstream EOF when no [DONE] arrives). Returning a Termination
	// cuts the stream there.
	OnEnd() (*Termination, error)
}

// TransformOptions tunes NewTransformedSSEStream.
type TransformOptions struct {
	// LookbehindChars withholds this many trailing characters of text per
	// choice so a pattern that spans two chunks is visible to the transformer
	// in one event. 0 disables re-segmentation. See NewTransformedSSEStream.
	LookbehindChars int
	// MinChunkChars collects the text deltas of a choice until at least this
	// many new characters (runes) are pending and presents them to the
	// transformer as one text event. 0 presents deltas as they arrive; values
	// above MaxMinChunkChars are clamped to it. See NewTransformedSSEStream.
	MinChunkChars int
	// MaxEventBytes bounds one SSE event. A larger event cannot be inspected
	// in flight, so the stream ends fail-closed with error code
	// "event_too_large" instead of relaying it past the transformer. 0
	// selects 4 MiB.
	MaxEventBytes int
	// OnError receives non-fatal problems (a replace on a non-text event, a
	// failed rewrite) and the error behind a fail-closed termination.
	OnError func(error)
}

// ErrStreamClosed is returned by Read after Close.
var ErrStreamClosed = errors.New("streaming: stream closed")

// ErrEventTooLarge reports that an upstream event exceeded MaxEventBytes.
var ErrEventTooLarge = errors.New("streaming: event exceeded the inspectable size")

const (
	transformReadBufferSize = 16 * 1024
	defaultMaxEventBytes    = 4 * 1024 * 1024
)

// MaxMinChunkChars caps TransformOptions.MinChunkChars: a policy cannot ask
// for more than this many characters of a choice's text to be collected
// before the transformer sees it. The run that reaches the threshold is
// presented whole, so a single large delta can carry more.
const MaxMinChunkChars = 16 * 1024

// NewTransformedSSEStream relays upstream through t. Reads are pull-based:
// each Read consumes upstream bytes, splits them into SSE events, calls t
// and returns the resulting bytes. Events t passes are relayed verbatim;
// comments and unparseable blocks are relayed without consulting t.
//
// A decision to terminate, an error from t, a Termination from OnEnd, or an
// event larger than MaxEventBytes ends the stream with the codec's terminal
// events (fail-closed with error code "plugin_failure" for errors and
// "event_too_large" for oversized events), closes upstream, and makes later
// Reads return io.EOF.
//
// Lookbehind re-segmentation (LookbehindChars = N > 0) applies to text
// deltas and to tool-call argument deltas, each kind in its own window: a
// choice's text is one window and each of its tool calls' arguments
// (Event.Call) another, with a withheld tail of at most N characters
// (runes), initially empty:
//
//  1. When a delta arrives, t sees one event of its kind whose Text is the
//     window tail+delta (Event.Overlap is the tail's length). Its decision
//     applies to the whole window: pass keeps it, replace substitutes
//     Decision.Text for it, drop discards it (tail included).
//  2. Of the resulting window, everything but the last N characters is
//     emitted to the client; the last N become the new tail.
//  3. An event that is not held (reasoning, finish, usage, other, a tool
//     call announced with empty arguments) first flushes every window, a
//     delta of another kind for the same choice flushes that choice's
//     windows of other kinds (its text before its first tool call), and
//     the upstream end flushes them all before OnEnd: t sees the tail once
//     more (Overlap equal to its length) and the result is emitted in
//     full. Windows of one kind (parallel tool calls) are independent and
//     may interleave without flushing each other.
//
// The first chunk of a window's run stays the template of its re-segmented
// events until something is emitted from it, so members only that chunk
// carries (a tool call's id and name sent with its first arguments) reach
// the client once.
//
// Consequently a pattern of up to N+1 characters is always visible to t in
// one event before any of its characters reaches the client, at the cost of
// N characters of delay.
//
// Coalescing (MinChunkChars = M > 0) collects the text deltas of a choice
// until at least M new characters are pending and only then runs step 1 on
// the window tail+pending, so t sees runs of at least M characters (the
// final run at a flush may be shorter). Both work together: the tail is
// what t already saw, the pending text is new, and Event.Overlap still
// counts the tail. Re-segmented events are rendered with RewriteText
// from the most recent raw chunk of that choice, so every other member of
// that chunk is preserved (a Responses event keeps its sequence_number).
// Members that must arrive once per choice (a chat chunk's finish_reason
// and usage, see Codec.StripTerminal) are left off the emitted head and
// travel with the chunk's withheld tail, so they follow the chunk's last
// text; when that text is dropped or emptied they go out on a chunk with
// empty text, so the stream still ends well-formed.
func NewTransformedSSEStream(upstream io.ReadCloser, codec Codec, t Transformer, opts TransformOptions) io.ReadCloser {
	if opts.MaxEventBytes <= 0 {
		opts.MaxEventBytes = defaultMaxEventBytes
	}
	if opts.MinChunkChars > MaxMinChunkChars {
		opts.MinChunkChars = MaxMinChunkChars
	}
	return &transformedSSEStream{
		upstream: upstream,
		codec:    codec,
		t:        t,
		opts:     opts,
		scanner:  EventScanner{MaxEventBytes: opts.MaxEventBytes},
		readBuf:  make([]byte, transformReadBufferSize),
		pending:  make(map[pendingKey]*pendingText),
	}
}

type transformedSSEStream struct {
	upstream io.ReadCloser
	codec    Codec
	t        Transformer
	opts     TransformOptions
	scanner  EventScanner
	readBuf  []byte

	out    []byte
	outPos int
	seq    int

	pending      map[pendingKey]*pendingText
	pendingOrder []pendingKey

	ended          bool
	endCalled      bool
	upstreamClosed bool
	closed         bool
	finalErr       error
}

// pendingKey identifies a window: a choice's text, or the arguments of one
// of its tool calls.
type pendingKey struct {
	choice int
	call   int
	kind   EventKind
}

func keyOf(ev Event) pendingKey {
	key := pendingKey{choice: ev.Choice, kind: ev.Kind}
	if ev.Kind == KindToolCallDelta {
		key.call = ev.Call
	}
	return key
}

// held reports whether ev is re-segmented rather than relayed: text deltas
// and tool-call deltas carrying arguments. A tool call announced with
// empty arguments passes through, so its id and name are relayed at once.
func held(ev Event) bool {
	return ev.Kind == KindTextDelta || (ev.Kind == KindToolCallDelta && ev.Text != "")
}

// pendingText is the withheld text of one window: the lookbehind tail the
// transformer already saw and the pending run it has not, together with the
// chunks re-segmented events are rendered from.
type pendingText struct {
	tail string
	// pending is text withheld under MinChunkChars that the transformer has
	// not seen yet; it follows tail in the next window.
	pending string
	queued  bool
	// template renders the choice's re-segmented events: the first chunk of
	// the pending run, so members it alone carries (a chat delta's role)
	// survive coalescing, and the latest chunk once a window was emitted,
	// so once-per-choice members ride with the withheld tail. head is the
	// template without those members; templateTerminal says it carries
	// some.
	template         Event
	head             Event
	templateTerminal bool
	// armed says the template is the first chunk of the run and nothing
	// was emitted from it yet, so it is kept until something is.
	armed   bool
	dataBuf []byte
	// closer is the latest chunk of the run that carried once-per-choice
	// members (a chat chunk's finish_reason and usage), owed to the client
	// while terminal is set.
	closer    Event
	closerBuf []byte
	terminal  bool
}

// setTemplate makes ev the chunk the choice's re-segmented events are
// rendered from.
func (s *transformedSSEStream) setTemplate(p *pendingText, ev Event) {
	p.dataBuf = append(p.dataBuf[:0], ev.Data...)
	p.template = ev
	p.template.Data = p.dataBuf
	p.head, p.templateTerminal = s.codec.StripTerminal(p.template)
}

// setCloser records ev as the chunk whose once-per-choice members the
// choice still owes the client.
func (s *transformedSSEStream) setCloser(p *pendingText, ev Event) {
	p.closerBuf = append(p.closerBuf[:0], ev.Data...)
	p.closer = ev
	p.closer.Data = p.closerBuf
	p.terminal = true
}

func (s *transformedSSEStream) Read(p []byte) (int, error) {
	if s.closed {
		return 0, ErrStreamClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	for s.outPos >= len(s.out) && !s.ended {
		s.out, s.outPos = s.out[:0], 0
		s.pump()
	}
	if s.outPos < len(s.out) {
		n := copy(p, s.out[s.outPos:])
		s.outPos += n
		return n, nil
	}
	return 0, s.finalErr
}

func (s *transformedSSEStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.closeUpstream()
}

func (s *transformedSSEStream) closeUpstream() error {
	if s.upstreamClosed {
		return nil
	}
	s.upstreamClosed = true
	return s.upstream.Close()
}

// pump reads one chunk from upstream and processes the events it completes.
func (s *transformedSSEStream) pump() {
	n, err := s.upstream.Read(s.readBuf)
	if n > 0 {
		for _, raw := range s.scanner.Feed(s.readBuf[:n]) {
			s.handle(raw)
			if s.ended {
				return
			}
		}
	}
	if err == nil {
		return
	}
	for _, raw := range s.scanner.Flush() {
		s.handle(raw)
		if s.ended {
			return
		}
	}
	s.flushPending()
	if !s.ended {
		s.callEnd()
	}
	if !s.ended {
		s.ended = true
		s.finalErr = err
		return
	}
	// The flush or OnEnd cut the stream. A real upstream failure still
	// outranks that clean ending: the response was truncated and must be
	// reported as such, not logged and billed as complete.
	if err != io.EOF {
		s.finalErr = err
	}
}

// handle processes one raw event, first splitting a multi-choice chunk so
// the transformer sees every choice. Comments are relayed untouched; an
// oversized event was never parsed, so relaying it would bypass t.
func (s *transformedSSEStream) handle(raw RawEvent) {
	if raw.Comment {
		s.write(raw.Raw)
		return
	}
	if raw.Oversized {
		s.report(ErrEventTooLarge)
		s.terminate(Termination{ErrorCode: "event_too_large", ErrorMessage: "a streamed event exceeded the size plugins can inspect"})
		return
	}
	parts := s.codec.Split(raw)
	if parts == nil {
		s.handleOne(raw)
		return
	}
	for _, part := range parts {
		s.handleOne(part)
		if s.ended {
			return
		}
	}
}

func (s *transformedSSEStream) handleOne(raw RawEvent) {
	ev := s.codec.Decode(raw, s.seq)
	if ev.Kind == KindDone {
		s.flushPending()
		if s.ended {
			return
		}
		s.callEnd()
		if s.ended {
			return
		}
		s.seq++
		s.write(raw.Raw)
		return
	}
	if (s.opts.LookbehindChars > 0 || s.opts.MinChunkChars > 0) && held(ev) {
		s.hold(ev)
		return
	}
	if len(s.pendingOrder) > 0 {
		s.flushPending()
		if s.ended {
			return
		}
		ev.Seq = s.seq
	}
	s.seq++
	// An event restating streamed text is rebuilt from what was emitted, so
	// a client never sees text a plugin replaced or dropped.
	if restated, changed := s.codec.Restate(ev); changed {
		s.apply(restated, nil)
		return
	}
	s.apply(ev, raw.Raw)
}

// apply consults the transformer and renders the outcome. raw, when set, is
// relayed verbatim on pass; otherwise ev is re-encoded.
func (s *transformedSSEStream) apply(ev Event, raw []byte) {
	decision, ok := s.decide(&ev)
	if !ok {
		return
	}
	switch decision.Action {
	case ActionDrop:
	case ActionReplace:
		rewritten, err := s.codec.RewriteText(ev, decision.Text)
		if err != nil {
			s.report(fmt.Errorf("streaming: replace on event %d (%s): %w", ev.Seq, ev.Kind, err))
			s.pass(ev, raw)
			return
		}
		s.codec.Track(rewritten)
		s.out = rewritten.appendEncoded(s.out)
	default:
		s.pass(ev, raw)
	}
}

// decide calls the transformer and handles the outcomes that end the stream
// (an error, or ActionTerminate); ok is false when the stream ended.
func (s *transformedSSEStream) decide(ev *Event) (Decision, bool) {
	decision, err := s.t.OnEvent(ev)
	if err != nil {
		s.fail(err)
		return decision, false
	}
	if decision.Action == ActionTerminate {
		var t Termination
		if decision.Terminate != nil {
			t = *decision.Terminate
		}
		s.terminate(t)
		return decision, false
	}
	return decision, true
}

func (s *transformedSSEStream) pass(ev Event, raw []byte) {
	s.codec.Track(ev)
	if raw != nil {
		s.write(raw)
		return
	}
	s.out = ev.appendEncoded(s.out)
}

func (s *transformedSSEStream) write(b []byte) {
	s.out = append(s.out, b...)
}

func (s *transformedSSEStream) report(err error) {
	if s.opts.OnError != nil {
		s.opts.OnError(err)
	}
}

// fail ends the stream closed after a transformer error.
func (s *transformedSSEStream) fail(err error) {
	s.report(err)
	s.terminate(Termination{ErrorCode: "plugin_failure", ErrorMessage: "stream transformer failed"})
}

func (s *transformedSSEStream) terminate(t Termination) {
	for _, chunk := range s.codec.Terminate(t) {
		s.write(chunk)
	}
	s.ended = true
	s.finalErr = io.EOF
	s.pendingOrder = nil
	_ = s.closeUpstream()
}

func (s *transformedSSEStream) callEnd() {
	if s.endCalled {
		return
	}
	s.endCalled = true
	t, err := s.t.OnEnd()
	if err != nil {
		s.fail(err)
		return
	}
	if t != nil {
		s.terminate(*t)
	}
}

// hold adds the delta to the pending text of its window and, once
// MinChunkChars are pending, shows the transformer the window tail+pending,
// emits all but the last N characters of the result and keeps the rest as
// the new tail. A delta of another kind for the same choice first flushes
// that choice's windows of other kinds, so text and tool calls keep their
// order.
func (s *transformedSSEStream) hold(ev Event) {
	key := keyOf(ev)
	s.flushOthers(key)
	if s.ended {
		return
	}
	p := s.pending[key]
	if p == nil {
		p = &pendingText{}
		s.pending[key] = p
	}
	if !p.queued {
		p.queued = true
		s.pendingOrder = append(s.pendingOrder, key)
	}
	if p.pending == "" && !p.armed {
		s.setTemplate(p, ev)
		p.armed = true
	}
	if _, terminal := s.codec.StripTerminal(ev); terminal {
		s.setCloser(p, ev)
	}

	p.pending += ev.Text
	if utf8.RuneCountInString(p.pending) < s.opts.MinChunkChars {
		return
	}
	window := p.tail + p.pending
	p.pending = ""
	result, ok := s.inspect(key, p, window, utf8.RuneCountInString(p.tail))
	if !ok {
		p.tail = ""
		return
	}
	head, tail := splitTail(result, s.opts.LookbehindChars)
	p.tail = tail
	if head != "" {
		s.emitText(key, p.head, head)
		p.armed = false
	}
	// The withheld tail continues under the latest chunk, so once-per-choice
	// members it carries go out with the choice's last text; a template
	// nothing was emitted from yet stays.
	if !p.armed {
		s.setTemplate(p, ev)
	}
}

// flushPending shows the transformer every window's tail (once more) and
// pending text and emits the results in full, in the order the windows were
// opened.
func (s *transformedSSEStream) flushPending() {
	for _, key := range s.pendingOrder {
		s.flushOne(key)
		if s.ended {
			return
		}
	}
	s.pendingOrder = s.pendingOrder[:0]
}

// flushOthers flushes the windows of key's choice that are of another kind.
func (s *transformedSSEStream) flushOthers(key pendingKey) {
	kept := s.pendingOrder[:0]
	for _, other := range s.pendingOrder {
		if other.choice != key.choice || other.kind == key.kind {
			kept = append(kept, other)
			continue
		}
		s.flushOne(other)
		if s.ended {
			return
		}
	}
	s.pendingOrder = kept
}

func (s *transformedSSEStream) flushOne(key pendingKey) {
	p := s.pending[key]
	if p == nil {
		return
	}
	p.queued = false
	window := p.tail + p.pending
	if window == "" {
		s.emitEnvelope(key, p)
		return
	}
	overlap := utf8.RuneCountInString(p.tail)
	p.tail, p.pending = "", ""
	result, ok := s.inspect(key, p, window, overlap)
	if s.ended {
		return
	}
	switch {
	case !ok || result == "":
		s.emitEnvelope(key, p)
	case p.terminal && !p.templateTerminal:
		// The run's template is not the chunk that carries the
		// once-per-choice members: the text goes out from it and the
		// owed chunk follows with empty text.
		s.emitText(key, p.head, result)
		s.emitEnvelope(key, p)
	default:
		s.emitText(key, p.template, result)
		p.terminal = false
	}
	p.armed = false
}

// emitEnvelope delivers the owed once-per-choice members when their chunk's
// text was dropped, emptied, or emitted from another template: the chunk
// goes out with empty text. Nothing is emitted when none are owed.
func (s *transformedSSEStream) emitEnvelope(key pendingKey, p *pendingText) {
	if !p.terminal {
		return
	}
	p.terminal = false
	ev := s.resegment(key, p.closer, "")
	s.codec.Track(ev)
	s.out = ev.appendEncoded(s.out)
}

// inspect hands text to the transformer as one event of the window's kind
// built from its template chunk and returns the text to emit; ok is false
// when nothing should be emitted (drop) or the stream ended.
func (s *transformedSSEStream) inspect(key pendingKey, p *pendingText, text string, overlap int) (string, bool) {
	ev := s.resegment(key, p.template, text)
	ev.Seq = s.seq
	ev.Overlap = overlap
	s.seq++
	decision, ok := s.decide(&ev)
	if !ok {
		return "", false
	}
	switch decision.Action {
	case ActionDrop:
		return "", false
	case ActionReplace:
		return decision.Text, true
	default:
		return text, true
	}
}

// emitText renders text as a re-segmented event of the window built from
// template.
func (s *transformedSSEStream) emitText(key pendingKey, template Event, text string) {
	if text == "" {
		return
	}
	ev := s.resegment(key, template, text)
	s.codec.Track(ev)
	s.out = ev.appendEncoded(s.out)
}

func (s *transformedSSEStream) resegment(key pendingKey, template Event, text string) Event {
	ev := template
	ev.Choice = key.choice
	if ev.Text == text {
		return ev
	}
	rewritten, err := s.codec.RewriteText(ev, text)
	if err != nil {
		s.report(fmt.Errorf("streaming: re-segment %s for choice %d: %w", ev.Kind, key.choice, err))
		ev.Text = text
		return ev
	}
	return rewritten
}

// splitTail splits text so tail holds its last n runes.
func splitTail(text string, n int) (head, tail string) {
	i := len(text)
	for k := 0; k < n && i > 0; k++ {
		_, size := utf8.DecodeLastRuneInString(text[:i])
		i -= size
	}
	return text[:i], text[i:]
}
