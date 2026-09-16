package presidio

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// mapping is the per-request table of numbered placeholders. The same
// value of one entity type gets the same placeholder everywhere in the
// request ("<PERSON_1>" for every "John Smith"), so the model sees a
// coherent conversation, and restore can put the values back. It lives in
// Exchange.Values from OnPrompt to the end of the stream.
type mapping struct {
	mu sync.Mutex
	// seq counts placeholders per entity type.
	seq map[string]int
	// byValue maps entity type + "\x00" + value to its placeholder.
	byValue map[string]string
	// byPlaceholder maps a placeholder to the original value.
	byPlaceholder map[string]string
	// restorable holds the placeholders that came from the prompt: only
	// those go back into the response, so a value the model produced
	// itself and that was anonymized on the way out stays anonymized.
	restorable map[string]bool
	// taken holds placeholder-shaped text already present in the request,
	// whose numbers are never allocated (see reserve).
	taken map[string]bool
}

// placeholderPattern matches text shaped like a placeholder this plugin
// allocates ("<PERSON_1>", "<EMAIL_ADDRESS_12>"), also with its angle
// brackets JSON-escaped ("\u003cPERSON_1\u003e"), as Gemini writes them in
// the tool-call arguments it returns. The name is group 1 (escaped) or
// group 2.
var placeholderPattern = regexp.MustCompile(`\\u003[cC]([A-Z][A-Z0-9_]*_[0-9]+)\\u003[eE]|<([A-Z][A-Z0-9_]*_[0-9]+)>`)

func newMapping() *mapping {
	return &mapping{seq: map[string]int{}, byValue: map[string]string{}, byPlaceholder: map[string]string{}, restorable: map[string]bool{}, taken: map[string]bool{}}
}

// reserve marks the placeholder-shaped tokens in text as taken so allocation
// skips their numbers. Without it a literal "<PERSON_2>" (typed by the user,
// or a placeholder an earlier response carried back unrestored) would share
// its placeholder with a new value, the model would see two people as one,
// and restore would put that value in place of the literal.
func (m *mapping) reserve(text string) {
	if !strings.Contains(text, "<") && !strings.Contains(text, `\u003`) {
		return
	}
	matches := placeholderPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, match := range matches {
		m.taken["<"+match[1]+match[2]+">"] = true
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
			p = fmt.Sprintf("<%s_%d>", entity, m.seq[entity])
			if !m.taken[p] {
				break
			}
		}
		m.byValue[key] = p
		m.byPlaceholder[p] = value
	}
	if restorable {
		m.restorable[p] = true
	}
	return p
}

// restore puts the original values back in place of the restorable
// placeholders in text and reports how many it replaced. Longer
// placeholders are replaced first so "<PERSON_1>" never matches inside
// "<PERSON_12>".
func (m *mapping) restore(text string) (string, int) {
	if m == nil || !strings.Contains(text, "<") {
		return text, 0
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.restorable))
	for p := range m.restorable {
		if strings.Contains(text, p) {
			keys = append(keys, p)
		}
	}
	values := make(map[string]string, len(keys))
	for _, p := range keys {
		values[p] = m.byPlaceholder[p]
	}
	m.mu.Unlock()
	if len(keys) == 0 {
		return text, 0
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	n := 0
	for _, p := range keys {
		c := strings.Count(text, p)
		if c == 0 {
			continue
		}
		text = strings.ReplaceAll(text, p, values[p])
		n += c
	}
	return text, n
}

// restoreJSON is restore for raw JSON text (streamed tool-call arguments):
// it also finds placeholders whose angle brackets are escaped, and writes
// each value JSON-escaped so the arguments stay valid JSON.
func (m *mapping) restoreJSON(text string) (string, int) {
	if m == nil || (!strings.Contains(text, "<") && !strings.Contains(text, `\u003`)) {
		return text, 0
	}
	matches := placeholderPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, 0
	}
	var b strings.Builder
	last, n := 0, 0
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, loc := range matches {
		escaped := loc[2] >= 0
		var name string
		if escaped {
			name = text[loc[2]:loc[3]]
		} else {
			name = text[loc[4]:loc[5]]
		}
		p := "<" + name + ">"
		// An escaped form preceded by an odd run of backslashes is the
		// literal text "\u003c...", not an escaped bracket.
		if !m.restorable[p] || (escaped && escapedAt(text, loc[0])) {
			continue
		}
		value, err := json.Marshal(m.byPlaceholder[p])
		if err != nil {
			continue
		}
		b.WriteString(text[last:loc[0]])
		b.Write(value[1 : len(value)-1])
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
