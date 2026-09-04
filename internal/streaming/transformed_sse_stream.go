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
	// Text is the replacement delta text for ActionReplace. Only text and
	// reasoning deltas can be replaced.
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
	// OnError receives non-fatal problems (a replace on a non-text event, a
	// failed rewrite) and the error behind a fail-closed termination.
	OnError func(error)
}

// ErrStreamClosed is returned by Read after Close.
var ErrStreamClosed = errors.New("streaming: stream closed")

const transformReadBufferSize = 16 * 1024

// NewTransformedSSEStream relays upstream through t. Reads are pull-based:
// each Read consumes upstream bytes, splits them into SSE events, calls t
// and returns the resulting bytes. Events t passes are relayed verbatim;
// comments and unparseable blocks are relayed without consulting t.
//
// A decision to terminate, an error from t, or a Termination from OnEnd ends
// the stream with the codec's terminal events (fail-closed with error code
// "plugin_failure" for errors), closes upstream, and makes later Reads
// return io.EOF.
//
// Lookbehind re-segmentation (LookbehindChars = N > 0) applies to text
// deltas only and works per choice with a withheld tail of at most N
// characters (runes), initially empty:
//
//  1. When a text delta arrives, t sees one text event whose Text is the
//     window tail+delta (Event.Overlap is the tail's length). Its decision
//     applies to the whole window: pass keeps it, replace substitutes
//     Decision.Text for it, drop discards it (tail included).
//  2. Of the resulting window, everything but the last N characters is
//     emitted to the client; the last N become the new tail.
//  3. A non-text event of any kind (tool call, finish, usage, other) first
//     flushes every choice's tail, and the upstream end flushes them before
//     OnEnd: t sees the tail once more as a text event (Overlap equal to
//     its length) and the result is emitted in full.
//
// Consequently a pattern of up to N+1 characters is always visible to t in
// one event before any of its characters reaches the client, at the cost of
// N characters of delay. Re-segmented events are rendered with RewriteText
// from the most recent raw chunk of that choice, so every other member of
// that chunk is preserved (a Responses event keeps its sequence_number).
func NewTransformedSSEStream(upstream io.ReadCloser, codec Codec, t Transformer, opts TransformOptions) io.ReadCloser {
	return &transformedSSEStream{
		upstream: upstream,
		codec:    codec,
		t:        t,
		opts:     opts,
		readBuf:  make([]byte, transformReadBufferSize),
		pending:  make(map[int]*pendingText),
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

	pending      map[int]*pendingText
	pendingOrder []int

	ended          bool
	endCalled      bool
	upstreamClosed bool
	closed         bool
	finalErr       error
}

// pendingText is the withheld tail of one choice under lookbehind together
// with the chunk used as the template for re-segmented events.
type pendingText struct {
	tail     string
	queued   bool
	template Event
	dataBuf  []byte
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
	if s.ended {
		return
	}
	s.callEnd()
	if s.ended {
		return
	}
	s.ended = true
	s.finalErr = err
}

func (s *transformedSSEStream) handle(raw RawEvent) {
	if raw.Comment || raw.Oversized {
		s.write(raw.Raw)
		return
	}
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
	if s.opts.LookbehindChars > 0 && ev.Kind == KindTextDelta {
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

// hold shows the transformer the window tail+delta of the event's choice,
// emits all but the last N characters of the result and keeps the rest as
// the new tail.
func (s *transformedSSEStream) hold(ev Event) {
	p := s.pending[ev.Choice]
	if p == nil {
		p = &pendingText{}
		s.pending[ev.Choice] = p
	}
	if !p.queued {
		p.queued = true
		s.pendingOrder = append(s.pendingOrder, ev.Choice)
	}
	p.dataBuf = append(p.dataBuf[:0], ev.Data...)
	p.template = ev
	p.template.Data = p.dataBuf

	window := p.tail + ev.Text
	result, ok := s.inspect(ev.Choice, p, window, utf8.RuneCountInString(p.tail))
	if !ok {
		p.tail = ""
		return
	}
	head, tail := splitTail(result, s.opts.LookbehindChars)
	p.tail = tail
	s.emitText(ev.Choice, p, head)
}

// flushPending shows the transformer every choice's tail once more and
// emits the results in full, in the order the tails were opened.
func (s *transformedSSEStream) flushPending() {
	for _, choice := range s.pendingOrder {
		p := s.pending[choice]
		if p == nil {
			continue
		}
		p.queued = false
		if p.tail == "" {
			continue
		}
		tail := p.tail
		p.tail = ""
		result, ok := s.inspect(choice, p, tail, utf8.RuneCountInString(tail))
		if s.ended {
			return
		}
		if ok {
			s.emitText(choice, p, result)
		}
	}
	s.pendingOrder = s.pendingOrder[:0]
}

// inspect hands text to the transformer as one text event built from the
// choice's template chunk and returns the text to emit; ok is false when
// nothing should be emitted (drop) or the stream ended.
func (s *transformedSSEStream) inspect(choice int, p *pendingText, text string, overlap int) (string, bool) {
	ev := s.resegment(choice, p, text)
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

// emitText renders text as a re-segmented event of the choice.
func (s *transformedSSEStream) emitText(choice int, p *pendingText, text string) {
	if text == "" {
		return
	}
	ev := s.resegment(choice, p, text)
	s.codec.Track(ev)
	s.out = ev.appendEncoded(s.out)
}

func (s *transformedSSEStream) resegment(choice int, p *pendingText, text string) Event {
	ev := p.template
	ev.Choice = choice
	if ev.Text == text {
		return ev
	}
	rewritten, err := s.codec.RewriteText(ev, text)
	if err != nil {
		s.report(fmt.Errorf("streaming: re-segment text for choice %d: %w", choice, err))
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
