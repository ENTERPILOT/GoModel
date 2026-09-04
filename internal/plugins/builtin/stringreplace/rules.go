package stringreplace

import (
	"fmt"
	"regexp"
	"strings"
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
// total number of replacements.
func apply(rules []rule, s string) (string, int) {
	total := 0
	for _, r := range rules {
		n := len(r.re.FindAllStringIndex(s, -1))
		if n == 0 {
			continue
		}
		total += n
		if r.literal {
			s = r.re.ReplaceAllLiteralString(s, r.replace)
		} else {
			s = r.re.ReplaceAllString(s, r.replace)
		}
	}
	return s, total
}

// count returns how many rule matches s contains without editing it.
func count(rules []rule, s string) int {
	total := 0
	for _, r := range rules {
		total += len(r.re.FindAllStringIndex(s, -1))
	}
	return total
}
