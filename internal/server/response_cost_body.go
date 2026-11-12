package server

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// maxUsageKeyBytes bounds how much of a top-level member name is kept to
// compare it with "usage"; the longest escaped spelling of it is 32 bytes.
const maxUsageKeyBytes = 64

// jsonUsageEditor applies an edit to every top-level usage object of a JSON
// body as the body passes through: like withUsageCost, every one named usage
// in any case loses an upstream's cost members, and every one named exactly
// usage then carries this gateway's cost. Only a usage object is held back, from
// its opening to its closing brace; every other byte is relayed as it
// arrives, so the body is neither buffered nor re-encoded.
type jsonUsageEditor struct {
	edit usageCostEdit

	started  bool // the body's first byte other than whitespace was read
	off      bool // the body is not an object: it is relayed as it is
	closed   bool // the body's object has ended; nothing after it is edited
	depth    int  // nesting of objects and arrays, outside strings
	inString bool
	escaped  bool

	expectKey bool   // the next string at depth 1 is a member name
	inKey     bool   // that string is being read
	key       []byte // its raw bytes, up to maxUsageKeyBytes
	afterKey  bool   // a name was read and its colon is due
	isUsage   bool   // the member being read is named usage, in any case
	carries   bool   // and exactly usage: its object carries the cost
	awaiting  bool   // the usage member's value is due

	capturing bool
	usage     []byte // the usage object held back
	out       []byte
}

// feed returns the bytes of p that can be relayed now.
func (e *jsonUsageEditor) feed(p []byte) []byte {
	e.out = e.out[:0]
	if e.off {
		return append(e.out, p...)
	}
	from := 0
	for i, c := range p {
		if !e.started && !isJSONSpace(c) {
			e.started = true
			if c != '{' {
				e.off = true
				return append(e.out, p...)
			}
		}
		if e.capturing {
			e.usage = append(e.usage, c)
			if e.step(c) {
				e.out = e.appendUsage(e.out)
			} else if len(e.usage) > streaming.MaxObservedEventBytes {
				// Too large to be a usage object; relayed as it is.
				e.out = append(e.out, e.usage...)
				e.capturing, e.usage = false, e.usage[:0]
			}
			from = i + 1
			continue
		}
		if e.awaiting && !isJSONSpace(c) {
			e.awaiting = false
			if c == '{' {
				e.out = append(e.out, p[from:i]...)
				e.capturing, e.usage = true, append(e.usage[:0], c)
				e.step(c)
				from = i + 1
				continue
			}
		}
		e.step(c)
	}
	if !e.capturing {
		e.out = append(e.out, p[from:]...)
	}
	return e.out
}

// flush returns what is still held back when the body ends: an unterminated
// usage object, relayed as it is.
func (e *jsonUsageEditor) flush() []byte {
	if !e.capturing {
		return nil
	}
	e.capturing = false
	return e.usage
}

func (e *jsonUsageEditor) appendUsage(out []byte) []byte {
	e.capturing = false
	if text, ok := e.edit.carrying(e.carries).apply(string(e.usage)); ok {
		return append(out, text...)
	}
	return append(out, e.usage...)
}

// step advances the scanner past c and reports whether c closed the usage
// object being held back.
func (e *jsonUsageEditor) step(c byte) bool {
	if e.inString {
		if e.inKey && len(e.key) <= maxUsageKeyBytes {
			e.key = append(e.key, c)
		}
		switch {
		case e.escaped:
			e.escaped = false
		case c == '\\':
			e.escaped = true
		case c == '"':
			e.inString = false
			if e.inKey {
				e.inKey, e.afterKey = false, true
				name := ""
				if len(e.key) <= maxUsageKeyBytes {
					name = gjson.ParseBytes(e.key).Str
				}
				e.isUsage, e.carries = strings.EqualFold(name, "usage"), name == "usage"
			}
		}
		return false
	}
	switch c {
	case '"':
		e.inString = true
		if e.topLevel() && e.expectKey {
			e.expectKey, e.inKey, e.key = false, true, append(e.key[:0], c)
		}
	case '{', '[':
		e.depth++
		e.expectKey = e.topLevel()
	case '}', ']':
		e.depth--
		e.closed = e.closed || e.depth == 0
		return e.capturing && e.depth == 1
	case ',':
		e.expectKey = e.topLevel()
	case ':':
		if e.topLevel() && e.afterKey {
			e.afterKey, e.awaiting = false, e.isUsage
		}
	}
	return false
}

// topLevel reports whether the scanner is among the members of the body's
// object.
func (e *jsonUsageEditor) topLevel() bool {
	return e.depth == 1 && !e.closed
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
