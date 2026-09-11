package presidio

import (
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
// allocates ("<PERSON_1>", "<EMAIL_ADDRESS_12>").
var placeholderPattern = regexp.MustCompile(`<[A-Z][A-Z0-9_]*_[0-9]+>`)

func newMapping() *mapping {
	return &mapping{seq: map[string]int{}, byValue: map[string]string{}, byPlaceholder: map[string]string{}, restorable: map[string]bool{}, taken: map[string]bool{}}
}

// reserve marks the placeholder-shaped tokens in text as taken so allocation
// skips their numbers. Without it a literal "<PERSON_2>" (typed by the user,
// or a placeholder an earlier response carried back unrestored) would share
// its placeholder with a new value, the model would see two people as one,
// and restore would put that value in place of the literal.
func (m *mapping) reserve(text string) {
	if !strings.Contains(text, "<") {
		return
	}
	tokens := placeholderPattern.FindAllString(text, -1)
	if len(tokens) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range tokens {
		m.taken[t] = true
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
