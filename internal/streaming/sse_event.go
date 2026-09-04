package streaming

import "bytes"

// EventKind classifies a decoded SSE event for stream transformers.
type EventKind string

const (
	KindTextDelta      EventKind = "text_delta"
	KindToolCallDelta  EventKind = "tool_call_delta"
	KindReasoningDelta EventKind = "reasoning_delta"
	KindFinish         EventKind = "finish"
	KindUsage          EventKind = "usage"
	KindOther          EventKind = "other"
	// KindDone is the "data: [DONE]" sentinel that ends OpenAI-style streams.
	KindDone EventKind = "done"
)

// Event is one decoded SSE event in a canonical (chat or Responses) stream.
type Event struct {
	// Seq is the zero-based position of the event in the stream, counting
	// decoded events only (comments and blank blocks are not numbered).
	Seq  int
	Kind EventKind
	// Choice is the chat choice index; always 0 for Responses streams.
	Choice int
	// Text is the delta text for text and reasoning deltas, and the arguments
	// fragment carried by a tool call delta. Empty for other kinds.
	Text string
	// Overlap is the number of leading characters (runes) of Text that were
	// already shown in the previous text event of this choice. It is non-zero
	// only for text deltas re-segmented under lookbehind, where consecutive
	// windows overlap; Text[Overlap:] is the new text.
	Overlap int
	// Name is the SSE "event:" field. Empty for chat chunks.
	Name string
	// Data is the JSON payload, or the literal [DONE]. For events handed to a
	// Transformer it is only valid during the call; copy it to retain it.
	Data []byte
}

// Encode renders the event as SSE: "event: <name>\n" when Name is set,
// followed by one "data:" line per line of Data and a blank line.
func (e *Event) Encode() []byte {
	out := make([]byte, 0, len(e.Name)+len(e.Data)+16)
	return e.appendEncoded(out)
}

func (e *Event) appendEncoded(out []byte) []byte {
	if e.Name != "" {
		out = append(out, "event: "...)
		out = append(out, e.Name...)
		out = append(out, '\n')
	}
	data := e.Data
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx == -1 {
			break
		}
		out = append(out, "data: "...)
		out = append(out, data[:idx]...)
		out = append(out, '\n')
		data = data[idx+1:]
	}
	out = append(out, "data: "...)
	out = append(out, data...)
	return append(out, '\n', '\n')
}

// RawEvent is one SSE block split off by EventScanner.
type RawEvent struct {
	// Name is the "event:" field, if any.
	Name string
	// Data joins the block's "data:" lines with "\n" (per the SSE spec). Nil
	// when the block carries no data field.
	Data []byte
	// Comment marks a block without a data field (a ":" comment, an id/retry
	// only block, or an empty block). Such blocks are relayed verbatim.
	Comment bool
	// Oversized marks a fragment of an event that exceeded MaxEventBytes; the
	// fragment is relayed unparsed and never decoded.
	Oversized bool
	// Raw holds the block's original bytes including its terminating blank
	// line (when one was seen), so pass-through can be byte-identical.
	Raw []byte
}

// EventScanner incrementally splits an SSE byte stream into events.
//
// Feed returns the events completed by the chunk; Flush returns the trailing
// partial block, if any, once the stream has ended. Slices in returned events
// alias the scanner's buffer or the fed chunk and are valid only until the
// next Feed or Flush call.
type EventScanner struct {
	// MaxEventBytes bounds the bytes held for an unterminated event. Once
	// exceeded the event is relayed as Oversized fragments until its boundary.
	// Zero selects 256 KiB.
	MaxEventBytes int

	pending    []byte
	discarding bool
	// tail keeps the last bytes already relayed while discarding so a
	// boundary split across chunks is still found.
	tail []byte
}

func (s *EventScanner) limit() int {
	if s.MaxEventBytes > 0 {
		return s.MaxEventBytes
	}
	return maxPendingEventBytes
}

// Feed consumes the next chunk of the stream and returns the completed events.
func (s *EventScanner) Feed(chunk []byte) []RawEvent {
	if len(chunk) == 0 {
		return nil
	}
	var events []RawEvent

	data := chunk
	aliased := false
	if s.discarding {
		idx, sepLen := nextJoinedEventBoundary(s.tail, chunk)
		if idx == -1 {
			s.tail = joinedSuffix(s.tail, chunk, maxBoundaryTailBytes)
			return []RawEvent{{Oversized: true, Raw: chunk}}
		}
		end := dataOffsetAfterBoundary(len(s.tail), idx, sepLen)
		events = append(events, RawEvent{Oversized: true, Raw: chunk[:end]})
		s.discarding = false
		s.tail = nil
		data = chunk[end:]
	} else if len(s.pending) > 0 {
		s.pending = append(s.pending, chunk...)
		data = s.pending
		aliased = true
	}

	for len(data) > 0 {
		idx, sepLen := nextEventBoundary(data)
		if idx == -1 {
			break
		}
		end := idx + sepLen
		events = append(events, parseRawEvent(data[:end], data[:idx]))
		data = data[end:]
	}

	switch {
	case len(data) == 0:
		s.pending = s.pending[:0]
	case len(data) > s.limit():
		events = append(events, RawEvent{Oversized: true, Raw: data})
		s.discarding = true
		s.tail = joinedSuffix(nil, data, maxBoundaryTailBytes)
		s.pending = s.pending[:0]
	case !aliased:
		s.pending = append(s.pending[:0], data...)
	case len(events) == 0:
		s.pending = data
	default:
		// Returned events alias the old buffer; leave it untouched.
		s.pending = append(make([]byte, 0, 2*len(data)), data...)
	}
	return events
}

// Flush returns the unterminated trailing block, if any, and resets the
// scanner. Call it once the upstream has ended.
func (s *EventScanner) Flush() []RawEvent {
	s.discarding = false
	s.tail = nil
	if len(s.pending) == 0 {
		return nil
	}
	block := s.pending
	s.pending = nil
	return []RawEvent{parseRawEvent(block, bytes.TrimRight(block, "\r\n"))}
}

var (
	eventPrefix   = []byte("event:")
	commentPrefix = []byte(":")
)

// parseRawEvent parses one block. raw is the block including its boundary;
// body excludes the boundary.
func parseRawEvent(raw, body []byte) RawEvent {
	ev := RawEvent{Raw: raw}
	// Fast path: a single data line, the common shape for chat chunks.
	if bytes.IndexByte(body, '\n') == -1 {
		parseEventLine(&ev, body)
	} else {
		var dataLines [][]byte
		for len(body) > 0 {
			line := body
			rest := []byte(nil)
			if idx := bytes.IndexByte(body, '\n'); idx != -1 {
				line, rest = body[:idx], body[idx+1:]
			}
			if data, ok := parseDataLine(line); ok {
				dataLines = append(dataLines, data)
			} else {
				parseEventLine(&ev, line)
			}
			body = rest
		}
		switch len(dataLines) {
		case 0:
		case 1:
			ev.Data = dataLines[0]
		default:
			ev.Data = bytes.Join(dataLines, []byte("\n"))
		}
	}
	ev.Comment = ev.Data == nil
	return ev
}

func parseEventLine(ev *RawEvent, line []byte) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	switch {
	case bytes.HasPrefix(line, dataPrefix):
		data, _ := parseDataLine(line)
		ev.Data = data
	case bytes.HasPrefix(line, eventPrefix):
		name := bytes.TrimPrefix(line, eventPrefix)
		if len(name) > 0 && name[0] == ' ' {
			name = name[1:]
		}
		ev.Name = string(name)
	case bytes.HasPrefix(line, commentPrefix):
		// SSE comment: ignored for parsing, relayed via Raw.
	}
}
