package stringreplace

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
)

// separator splits a rule line into its find and replace sides.
const separator = " => "

// rule is one compiled "find => replace" line.
type rule struct {
	find    string // as written, for summaries
	re      *regexp.Regexp
	replace string
	literal bool
}

// parseRules compiles the rule lines. Blank lines and lines starting with #
// are ignored. In literal mode both sides understand the escapes \n, \t,
// and \\. In regex mode the find side is passed to RE2 as written (it has
// its own escapes) and only the replace side is unescaped; $1 refers to a
// capture group.
func parseRules(entries []string, mode string, caseInsensitive bool) ([]rule, error) {
	var rules []rule
	for i, line := range entries {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		r, err := parseRule(line, mode, caseInsensitive)
		if err != nil {
			return nil, fmt.Errorf("string_replace: rules line %d: %w", i+1, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func parseRule(line, mode string, caseInsensitive bool) (rule, error) {
	find, replace, ok := strings.Cut(line, separator)
	if !ok {
		// Allow a trailing separator with an empty replacement even when
		// the editor trimmed the trailing space.
		if before, ok0 := strings.CutSuffix(line, strings.TrimRight(separator, " ")); ok0 {
			find, ok = before, true
		}
		if !ok {
			return rule{}, fmt.Errorf("expected \"find => replace\", got %q", strings.TrimSpace(line))
		}
	}
	find = strings.TrimRight(find, " ")
	find = strings.TrimLeft(find, " ")
	if find == "" {
		return rule{}, fmt.Errorf("find side is empty in %q", strings.TrimSpace(line))
	}
	replace = unescape(strings.TrimRight(replace, "\r"))
	r := rule{find: find, replace: replace, literal: mode == ModeLiteral}
	pattern := find
	if r.literal {
		pattern = regexp.QuoteMeta(unescape(find))
	}
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return rule{}, fmt.Errorf("invalid regex %q: %w", find, err)
	}
	if re.MatchString("") {
		return rule{}, fmt.Errorf("pattern %q matches the empty string", find)
	}
	r.re = re
	return r, nil
}

// unescape expands \n, \t, and \; any other backslash sequence is kept.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

// apply runs every rule in order over s and returns the result with the
// total number of replacements. Matches that end within the first skip
// bytes are left alone: that prefix is streamed text an earlier window
// already transformed (see pluginapi.StreamEvent.Overlap).
func apply(rules []rule, s string, skip int) (string, int) {
	total := 0
	for _, r := range rules {
		out, n, first := r.replaceAfter(s, skip)
		if n == 0 {
			continue
		}
		total += n
		s = out
		// Everything from the first edit on is fresh output; the next rule
		// may match it.
		skip = min(skip, first)
	}
	return s, total
}

// replaceAfter applies r to every match ending after skip and returns the
// result, the number of replacements, and the start of the first one.
func (r rule) replaceAfter(s string, skip int) (string, int, int) {
	var b strings.Builder
	last, n, first := 0, 0, -1
	for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
		if m[1] <= skip {
			continue
		}
		if first < 0 {
			first = m[0]
		}
		b.WriteString(s[last:m[0]])
		if r.literal {
			b.WriteString(r.replace)
		} else {
			b.Write(r.re.ExpandString(nil, r.replace, s, m))
		}
		last = m[1]
		n++
	}
	if n == 0 {
		return s, 0, -1
	}
	b.WriteString(s[last:])
	return b.String(), n, first
}

// count returns how many rule matches end after the first skip bytes of s
// without editing it.
func count(rules []rule, s string, skip int) int {
	total := 0
	for _, r := range rules {
		for _, m := range r.re.FindAllStringIndex(s, -1) {
			if m[1] > skip {
				total++
			}
		}
	}
	return total
}

// overlapBytes converts the event's Overlap (runes) to a byte offset in
// its text.
func overlapBytes(ev *pluginapi.StreamEvent) int {
	if ev == nil || ev.Overlap <= 0 {
		return 0
	}
	offset := 0
	for i := 0; i < ev.Overlap && offset < len(ev.Text); i++ {
		_, size := utf8.DecodeRuneInString(ev.Text[offset:])
		offset += size
	}
	return offset
}
