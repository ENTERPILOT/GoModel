package server

import (
	"bytes"
	"io"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// maxHeldUsageBytes bounds the usage object a jsonUsageEditor holds back. A
// larger one is relayed as it is: it is not rewritten, so it gets no cost and
// keeps every member an upstream put in it.
const maxHeldUsageBytes = streaming.MaxObservedEventBytes

// maxKeptEmitBytes bounds the buffer kept between two rewritten pieces.
const maxKeptEmitBytes = 64 << 10

// jsonUsageEditor applies an edit to a JSON body as the body passes through,
// the way withUsageCost applies it to an event: the usage objects at the top
// level, under "response" and under "message" lose an upstream's cost members,
// the ones named exactly usage (at the top level or under exactly response)
// then carry this gateway's cost, and with a billed edit a provider's cost at
// the top level is removed.
//
// Only a usage object is held back, from its opening to its closing brace,
// and with a billed edit the few bytes between two top-level members until
// the second is named. Every other byte is written as it arrives, straight
// from the caller's buffer, so the body is neither buffered, copied nor
// re-encoded. What is written depends on the body's bytes alone, never on how
// the writes split them.
type jsonUsageEditor struct {
	edit usageCostEdit
	// maxUsage bounds a held usage object; zero selects maxHeldUsageBytes.
	maxUsage int

	started  bool // the body's first byte other than whitespace was read
	done     bool // nothing more is edited: the body is no object, or its object ended
	depth    int  // nesting of objects and arrays, outside strings
	inString bool
	escaped  bool

	// Member names are read at depth 1, among the members of the body's
	// object, and at depth 2 inside its response or message object.
	envelope        bool // inside that object
	envelopeCarries bool // which is named exactly response
	expectKey       bool // the next string is a member name
	inKey           bool // that string is being read
	key             []byte
	afterKey        bool       // a name was read and its colon is due
	awaiting        bool       // its value is due
	member          memberName // which member it is
	exact           bool       // and whether its name is spelled exactly

	capturing    bool
	captureDepth int    // the depth the held usage object closes at
	carries      bool   // it carries the cost
	usage        []byte // the usage object held back

	members rootMembers
	emit    []byte // bytes to write in place of the byte just read
}

// feed writes to dst the bytes of p that can be relayed now.
func (e *jsonUsageEditor) feed(dst io.Writer, p []byte) error {
	from := 0 // p[from:i] is relayed as it is, and not written yet
	for i := 0; i < len(p) && !e.done; i++ {
		if e.inString && !e.escaped && !e.inKey && !e.capturing && !e.members.dropping {
			// The bytes of a string up to its end or an escape are relayed
			// and change nothing: a long one (an image) is not read bytewise.
			if i += stringSpan(p[i:]); i == len(p) {
				break
			}
		}
		relayed := e.read(p[i])
		if relayed && len(e.emit) == 0 {
			continue
		}
		if err := writeBytes(dst, p[from:i], e.emit); err != nil {
			return err
		}
		if e.emit = e.emit[:0]; cap(e.emit) > maxKeptEmitBytes {
			e.emit = nil // the buffer of a usage object over the limit
		}
		if from = i; !relayed {
			from = i + 1
		}
	}
	return writeBytes(dst, p[from:])
}

// flush returns what is still held back when the body ends, as it is: an
// unterminated usage object, or the bytes in front of an unnamed member.
func (e *jsonUsageEditor) flush() []byte {
	var held []byte
	switch {
	case e.capturing:
		held = e.usage
	case e.members.holding:
		held = e.members.held
	}
	e.capturing, e.members.holding, e.done = false, false, true
	return held
}

func writeBytes(dst io.Writer, parts ...[]byte) error {
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if _, err := dst.Write(part); err != nil {
			return err
		}
	}
	return nil
}

// read advances the scanner past c and reports whether c is relayed as it is.
// A byte that is not was held back, removed, or written as part of e.emit.
func (e *jsonUsageEditor) read(c byte) bool {
	switch {
	case e.capturing:
		e.capture(c)
		return false
	case e.inString:
		return e.readString(c)
	case !e.started:
		if isJSONSpace(c) {
			return true
		}
		e.started = true
		if c != '{' {
			e.done = true
			return true
		}
	}
	return e.readStructure(c)
}

// capture holds back a byte of a usage object, and rewrites the object once c
// closes it.
func (e *jsonUsageEditor) capture(c byte) {
	e.usage = append(e.usage, c)
	switch {
	case e.inString:
		e.readStringByte(c)
	case c == '"':
		e.inString = true
	case c == '{', c == '[':
		e.depth++
	case c == '}', c == ']':
		e.depth--
	}
	limit := e.maxUsage
	if limit == 0 {
		limit = maxHeldUsageBytes
	}
	switch {
	case e.depth == e.captureDepth:
		e.capturing = false
		if text, ok := e.edit.carrying(e.carries).apply(string(e.usage)); ok {
			e.emit = append(e.emit, text...)
		} else {
			e.emit = append(e.emit, e.usage...)
		}
		if e.depth == 1 {
			e.members.valueEnded(e.edit.billed)
		}
	case len(e.usage) > limit:
		// Too large to be a usage object: relayed as it is, and its buffer
		// released.
		e.capturing = false
		e.emit, e.usage = e.usage, nil
	}
}

// readStringByte advances past a byte of a string and reports whether it
// closed the string.
func (e *jsonUsageEditor) readStringByte(c byte) bool {
	switch {
	case e.escaped:
		e.escaped = false
	case c == '\\':
		e.escaped = true
	case c == '"':
		e.inString = false
		return true
	}
	return false
}

// stringSpan returns how many leading bytes of data neither end a string nor
// start an escape in it.
func stringSpan(data []byte) int {
	end := bytes.IndexByte(data, '"')
	if end == -1 {
		end = len(data)
	}
	if escape := bytes.IndexByte(data[:end], '\\'); escape != -1 {
		return escape
	}
	return end
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
