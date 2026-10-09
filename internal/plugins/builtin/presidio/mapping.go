package presidio

import (
	"encoding/json"
	"strings"
	"sync"
)

// mapping is the per-request table of numbered placeholders. The same
// value of one entity type gets the same placeholder everywhere in the
// request ("<PERSON_1>" for every "John Smith"), so the model sees a
// coherent conversation, and restore can put the values back. It lives in
// Exchange.Values from OnPrompt to the end of the stream. Placeholders are
// keyed by [placeholderFormat.key], so every spelling of one finds it.
type mapping struct {
	mu sync.Mutex
	// format renders and finds the placeholders: the format of the
	// instance that created the table.
	format *placeholderFormat
	// seq counts placeholders per entity type.
	seq map[string]int
	// byValue maps entity type + "\x00" + value to its placeholder.
	byValue map[string]string
	// byPlaceholder maps a placeholder key to the original value.
	byPlaceholder map[string]string
	// restorable holds the keys of the placeholders that came from the
	// prompt roles restore covers: only those go back into the response, so
	// a value the model produced itself and that was anonymized on the way
	// out stays anonymized.
	restorable map[string]bool
	// taken holds the keys of placeholder-shaped text already present in
	// the request, whose numbers are never allocated (see reserve).
	taken map[string]bool
}

func newMapping(format *placeholderFormat) *mapping {
	return &mapping{format: format, seq: map[string]int{}, byValue: map[string]string{}, byPlaceholder: map[string]string{}, restorable: map[string]bool{}, taken: map[string]bool{}}
}

// reserve marks the placeholder-shaped tokens in text, in any spelling, as
// taken so allocation skips their numbers. Without it a literal
// "<PERSON_2>" (typed by the user, or a placeholder an earlier response
// carried back unrestored) would share its placeholder with a new value,
// the model would see two people as one, and restore would put that value
// in place of the literal.
func (m *mapping) reserve(text string) {
	if !m.format.mayContain(text) {
		return
	}
	matches := m.format.any.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, loc := range matches {
		m.taken[m.format.matchKey(text, loc)] = true
	}
}

// placeholder returns the placeholder for value as entity, allocating the
// next number of the type on first sight. restorable marks it so; a value
// first seen where it is not restorable (a system message) becomes
// restorable once the user sends it too.
func (m *mapping) placeholder(entity, value string, restorable bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := entity + "\x00" + value
	p, ok := m.byValue[key]
	if !ok {
		for {
			m.seq[entity]++
			p = m.format.render(entity, m.seq[entity])
			if !m.taken[m.format.key(p)] {
				break
			}
		}
		m.byValue[key] = p
		m.byPlaceholder[m.format.key(p)] = value
	}
	if restorable {
		m.restorable[m.format.key(p)] = true
	}
	return p
}

// restore puts the original values back in place of the restorable
// placeholders in text, in any spelling, and reports how many it replaced.
func (m *mapping) restore(text string) (string, int) {
	return m.restoreWith(text, false)
}

// restoreJSON is restore for raw JSON text (streamed tool-call arguments):
// it also finds placeholders whose punctuation is \u-escaped, and writes
// each value JSON-escaped so the arguments stay valid JSON.
func (m *mapping) restoreJSON(text string) (string, int) {
	return m.restoreWith(text, true)
}

func (m *mapping) restoreWith(text string, raw bool) (string, int) {
	if m == nil || !m.format.mayContain(text) {
		return text, 0
	}
	pattern := m.format.text
	if raw {
		pattern = m.format.json
	}
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, 0
	}
	var b strings.Builder
	last, n := 0, 0
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, loc := range matches {
		key := m.format.matchKey(text, loc)
		if !m.restorable[key] {
			continue
		}
		// In JSON, a match starting with a backslash that is itself
		// escaped is literal text ("\\u003c..."), not an escape.
		if raw && text[loc[0]] == '\\' && escapedAt(text, loc[0]) {
			continue
		}
		value := m.byPlaceholder[key]
		if raw {
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}
			value = string(encoded[1 : len(encoded)-1])
		}
		b.WriteString(text[last:loc[0]])
		b.WriteString(value)
		last = loc[1]
		n++
	}
	if n == 0 {
		return text, 0
	}
	b.WriteString(text[last:])
	return b.String(), n
}

// escapedAt reports whether the backslash at i is itself escaped by the
// backslashes before it.
func escapedAt(text string, i int) bool {
	run := 0
	for i--; i >= 0 && text[i] == '\\'; i-- {
		run++
	}
	return run%2 == 1
}

// hasRestorable reports whether any prompt placeholder exists, in which
// case the response carries request-specific data.
func (m *mapping) hasRestorable() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.restorable) > 0
}
