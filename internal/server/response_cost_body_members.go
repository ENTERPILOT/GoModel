package server

import (
	"strings"

	"github.com/tidwall/gjson"
)

// maxMemberNameBytes bounds how much of a member name is kept to tell which
// member it is. The longest escaped spelling of the longest name read,
// "response", is 50 bytes.
const maxMemberNameBytes = 64

// memberName is what the name of a member makes of its value.
type memberName uint8

const (
	memberOther    memberName = iota
	memberUsage               // usage, in any case: its object is rewritten
	memberEnvelope            // response or message: its usage member is read
	memberCost                // a provider's cost at the top level: removed
)

// valueKind is how the value of a top-level member ends.
type valueKind uint8

const (
	noValue     valueKind = iota
	scalarValue           // before the next whitespace, comma or brace
	stringValue           // with its closing quote
	nestedValue           // with the brace or bracket that closes it
)

// rootMembers removes members of the body's object, for a billed edit. The
// bytes between two members (whitespace, the comma) and the name that follows
// are held until the name tells whether the member stays; a removed member is
// not held but skipped as it arrives. The result is the one
// usageCostEdit.apply gives: a removed member takes the separator in front of
// it along, and the first member kept has none.
type rootMembers struct {
	value    valueKind // the value being read at depth 1
	holding  bool
	held     []byte // the separator, then the name as far as it was read
	nameAt   int    // where the name starts in held
	kept     bool   // a member before this one was kept
	dropping bool   // the member being read is removed
}

// valueEnded is called when the value of a top-level member ended.
func (m *rootMembers) valueEnded(billed bool) {
	m.value, m.dropping = noValue, false
	if billed {
		m.holding, m.held, m.nameAt = true, m.held[:0], 0
	}
}

// keep returns the held bytes of a member that stays, and stops holding.
func (m *rootMembers) keep() []byte {
	held := m.held
	if !m.kept {
		held = held[m.nameAt:]
	}
	m.holding, m.kept = false, true
	return held
}

// readString advances past a byte of a string that is not held as part of a
// usage object, and reports whether the byte is relayed as it is.
func (e *jsonUsageEditor) readString(c byte) bool {
	closed := e.readStringByte(c)
	m := &e.members
	if !e.inKey {
		relayed := !m.dropping
		if closed && e.depth == 1 && m.value == stringValue {
			m.valueEnded(e.edit.billed)
		}
		return relayed
	}
	if len(e.key) <= maxMemberNameBytes {
		e.key = append(e.key, c)
	}
	held := m.holding
	if held {
		m.held = append(m.held, c)
	}
	switch {
	case closed:
		e.inKey, e.afterKey = false, true
		e.nameMember()
		switch {
		case !held:
		case e.member == memberCost:
			m.holding, m.dropping = false, true
		default:
			e.emit = append(e.emit, m.keep()...)
		}
	case held && len(e.key) > maxMemberNameBytes:
		// Too long to be a name that is read.
		e.emit = append(e.emit, m.keep()...)
	}
	return !held
}

// nameMember tells which member the name just read names. Names are compared
// the way a lenient decoder matches them: unescaped and in any case.
func (e *jsonUsageEditor) nameMember() {
	name := ""
	if len(e.key) <= maxMemberNameBytes {
		name = gjson.ParseBytes(e.key).Str
	}
	e.member, e.exact = memberOther, false
	switch {
	case strings.EqualFold(name, "usage"):
		e.member, e.exact = memberUsage, name == "usage"
	case e.depth == 1 && isUsageEnvelope(name):
		e.member, e.exact = memberEnvelope, name == "response"
	case e.depth == 1 && e.edit.dropsRootMember(name):
		e.member = memberCost
	}
}

// readStructure advances past a byte outside strings and reports whether it
// is relayed as it is.
func (e *jsonUsageEditor) readStructure(c byte) bool {
	m := &e.members
	if e.awaiting && !isJSONSpace(c) {
		e.awaiting = false
		if e.startValue(c) {
			return false
		}
	}
	if e.depth == 1 && m.value == scalarValue && (isJSONSpace(c) || c == ',' || c == '}') {
		m.valueEnded(e.edit.billed)
	}
	relayed := !m.dropping
	switch c {
	case '"':
		e.inString = true
		if !e.expectKey {
			break
		}
		e.expectKey, e.inKey, e.key = false, true, append(e.key[:0], c)
		if e.depth != 1 || !e.edit.billed {
			return relayed
		}
		if !m.holding { // the first member: no separator in front of it
			m.holding, m.held = true, m.held[:0]
		}
		m.nameAt = len(m.held)
		m.held = append(m.held, c)
		return false
	case '{', '[':
		e.depth++
		e.expectKey = c == '{' && e.readsNames()
	case '}', ']':
		e.depth--
		e.expectKey = false
		switch e.depth {
		case 0:
			e.done = true
		case 1:
			e.envelope = false
			if m.value == nestedValue {
				m.valueEnded(e.edit.billed)
				return relayed
			}
		}
	case ',':
		e.expectKey = e.readsNames()
	case ':':
		if e.afterKey && e.readsNames() {
			e.afterKey, e.awaiting = false, true
		}
	}
	if m.holding {
		if isJSONSpace(c) || c == ',' {
			m.held = append(m.held, c)
			return false
		}
		// The brace that closes the body's object, or a byte no JSON has
		// here: what was held is no separator in front of a removed member.
		e.emit = append(e.emit, m.held...)
		m.holding = false
	}
	return relayed
}

// readsNames reports whether the scanner is among members whose names it
// reads.
func (e *jsonUsageEditor) readsNames() bool {
	return e.depth == 1 || (e.depth == 2 && e.envelope)
}

// startValue starts the value of the member just named, whose first byte is
// c, and reports whether c was held back as the start of a usage object.
func (e *jsonUsageEditor) startValue(c byte) bool {
	m := &e.members
	if e.depth == 1 {
		switch c {
		case '{', '[':
			m.value = nestedValue
		case '"':
			m.value = stringValue
		default:
			m.value = scalarValue
		}
	}
	if c != '{' || m.dropping {
		return false
	}
	switch e.member {
	case memberUsage:
		e.capturing, e.captureDepth = true, e.depth
		e.carries = e.exact && (e.depth == 1 || e.envelopeCarries)
		e.usage = append(e.usage[:0], c)
		e.depth++
		return true
	case memberEnvelope:
		e.envelope, e.envelopeCarries = true, e.exact
	}
	return false
}
