package presidio

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/pluginapi"
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
	// restorable maps the keys of the placeholders that came from the
	// prompt of an instance with restore on to the roles of the messages
	// holding their value, each with the grant's chosen flag. Only those go
	// back into the response, and only for the roles the restoring instance
	// allows, so a value the model produced itself and that was anonymized
	// on the way out stays anonymized.
	restorable map[string]map[pluginapi.Role]bool
	// taken holds the keys of placeholder-shaped text already present in
	// the request, whose numbers are never allocated (see reserve).
	taken map[string]bool
}

func newMapping(format *placeholderFormat) *mapping {
	return &mapping{format: format, seq: map[string]int{}, byValue: map[string]string{}, byPlaceholder: map[string]string{}, restorable: map[string]map[pluginapi.Role]bool{}, taken: map[string]bool{}}
}

// grant makes the placeholders of one prompt role's values restorable. role
// is empty for values that are never restored. chosen marks a role the
// restore_roles of the prompt instance names; without restore_roles there,
// the restoring instance alone decides.
type grant struct {
	role   pluginapi.Role
	chosen bool
}

// roleFilter is the restore_roles of a restoring instance: its roles (the
// default list when it sets none) and whether it sets them.
type roleFilter struct {
	roles map[pluginapi.Role]bool
	set   bool
}

// allows reports whether a value granted for role comes back: restore_roles
// is honored on whichever instance sets it, the default list applies when
// neither does.
func (f roleFilter) allows(role pluginapi.Role, chosen bool) bool {
	return f.roles[role] || (chosen && !f.set)
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
// next free number of the type on first sight: one neither taken nor
// already standing for another value, which a type differing only in case
// would otherwise share. g makes it restorable for its role; a value first
// seen where it is not restorable (a system message) becomes restorable
// once the user sends it too.
func (m *mapping) placeholder(entity, value string, g grant) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := entity + "\x00" + value
	p, ok := m.byValue[key]
	if !ok {
		for {
			m.seq[entity]++
			p = m.format.render(entity, m.seq[entity])
			_, used := m.byPlaceholder[m.format.key(p)]
			if !used && !m.taken[m.format.key(p)] {
				break
			}
		}
		m.byValue[key] = p
		m.byPlaceholder[m.format.key(p)] = value
	}
	if g.role != "" {
		k := m.format.key(p)
		if m.restorable[k] == nil {
			m.restorable[k] = map[pluginapi.Role]bool{}
		}
		m.restorable[k][g.role] = m.restorable[k][g.role] || g.chosen
	}
	return p
}

// restorableFor reports whether the placeholder key comes back under f.
// The caller holds m.mu.
func (m *mapping) restorableFor(key string, f roleFilter) bool {
	for role, chosen := range m.restorable[key] {
		if f.allows(role, chosen) {
			return true
		}
	}
	return false
}

// restore puts the original values back in place of the placeholders in
// text, in any spelling, that are restorable under f, and reports how many
// it replaced.
func (m *mapping) restore(text string, f roleFilter) (string, int) {
	return m.restoreWith(text, f, false)
}

// restoreJSON is restore for raw JSON text (streamed tool-call arguments):
// it also finds placeholders whose punctuation is \u-escaped, and writes
// each value JSON-escaped so the arguments stay valid JSON.
func (m *mapping) restoreJSON(text string, f roleFilter) (string, int) {
	return m.restoreWith(text, f, true)
}

func (m *mapping) restoreWith(text string, f roleFilter, raw bool) (string, int) {
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
		if !m.restorableFor(key, f) {
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
