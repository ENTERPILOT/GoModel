package presidio

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DefaultPlaceholderFormat is how replace writes a placeholder unless
// placeholder_format says otherwise: "<PERSON_1>".
const DefaultPlaceholderFormat = "<{entity}_{n}>"

const (
	entityToken = "{entity}"
	numberToken = "{n}"
)

// placeholderFormat renders numbered placeholders and finds them again in
// the spellings models and renderers turn them into: any letter case,
// Markdown-escaped ("\<PERSON\_1\>"), HTML-escaped ("&lt;PERSON_1&gt;"),
// and, in raw JSON, with \u-escaped punctuation ("<PERSON_1>").
type placeholderFormat struct {
	format string
	// text finds placeholders in plain text, json in raw JSON text
	// (streamed tool-call arguments), and any in either, for reserving.
	// The groups capture the entity type and the number, in the order
	// the format names them (numberFirst).
	text, json, any *regexp.Regexp
	numberFirst     bool
	// hints holds bytes one of which every match contains.
	hints string
}

type patternMode int

const (
	modeText patternMode = iota
	modeJSON
	modeAny
)

// htmlEntities are the escapes HTML sanitizers write for the punctuation a
// placeholder format may use.
var htmlEntities = map[byte]string{'<': "&lt;", '>': "&gt;", '&': "&amp;"}

// parsePlaceholderFormat validates format: one {entity} and one {n} with
// something between them, the rest printable ASCII other than \, ", { and },
// starting and ending with a punctuation character so a placeholder has
// clear edges.
func parsePlaceholderFormat(format string) (*placeholderFormat, error) {
	if strings.Count(format, entityToken) != 1 || strings.Count(format, numberToken) != 1 {
		return nil, fmt.Errorf("must contain %s and %s once each", entityToken, numberToken)
	}
	// Adjacent, PERSON1 number 1 and PERSON number 11 would look the same.
	if strings.Contains(format, entityToken+numberToken) || strings.Contains(format, numberToken+entityToken) {
		return nil, fmt.Errorf("must separate %s and %s, such as %s", entityToken, numberToken, DefaultPlaceholderFormat)
	}
	literal := strings.NewReplacer(entityToken, "", numberToken, "").Replace(format)
	for i := 0; i < len(literal); i++ {
		c := literal[i]
		if c < 0x20 || c > 0x7e || strings.IndexByte(`\"{}`, c) >= 0 {
			return nil, fmt.Errorf("may only add printable ASCII other than \\, \", { and }")
		}
	}
	// A format starting or ending with a token has a brace there.
	first, last := format[0], format[len(format)-1]
	if !punct(first) || !punct(last) || first == '{' || last == '}' {
		return nil, fmt.Errorf("must start and end with a punctuation character, such as <...> or [...]")
	}
	f := &placeholderFormat{format: format, hints: string(format[0]) + `\&`, numberFirst: strings.Index(format, numberToken) < strings.Index(format, entityToken)}
	f.text = regexp.MustCompile(f.pattern(modeText))
	f.json = regexp.MustCompile(f.pattern(modeJSON))
	f.any = regexp.MustCompile(f.pattern(modeAny))
	return f, nil
}

// punct reports whether c is ASCII punctuation (the underscore included),
// which Markdown may escape with a backslash.
func punct(c byte) bool {
	alnum := '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
	return c > 0x20 && c < 0x7f && !alnum
}

// pattern compiles the format into a case-insensitive expression for mode.
func (f *placeholderFormat) pattern(mode patternMode) string {
	var b strings.Builder
	b.WriteString("(?i)")
	for rest := f.format; rest != ""; {
		switch {
		case strings.HasPrefix(rest, entityToken):
			// Underscores inside the type may be Markdown-escaped too.
			fmt.Fprintf(&b, "((?:%s|%s)+)", f.entityClass(), literalPattern('_', mode))
			rest = rest[len(entityToken):]
		case strings.HasPrefix(rest, numberToken):
			b.WriteString("([0-9]+)")
			rest = rest[len(numberToken):]
		default:
			b.WriteString(literalPattern(rest[0], mode))
			rest = rest[1:]
		}
	}
	return b.String()
}

// entityClass matches one character of an entity type. Ad hoc recognizers
// may name types with any characters, so it takes everything but
// whitespace, the characters escapes start with, and the punctuation that
// can mark where the type starts or ends: the format's prefix and what
// follows {entity}. Punctuation only between {n} and {entity}, such as the
// "." of "[{n}.{entity}]", may occur in the type: the number bounds it.
func (f *placeholderFormat) entityClass() string {
	entity, number := strings.Index(f.format, entityToken), strings.Index(f.format, numberToken)
	edges := f.format[:min(entity, number)] + f.format[entity+len(entityToken):]
	edges = strings.Replace(edges, numberToken, "", 1)
	var b strings.Builder
	b.WriteString(`[^\s\\"&;`)
	for i := 0; i < len(edges); i++ {
		if punct(edges[i]) {
			// Escaped, so "-" cannot form a range.
			b.WriteByte('\\')
			b.WriteByte(edges[i])
		}
	}
	b.WriteString("]")
	return b.String()
}

// literalPattern matches one literal character of the format and the
// escaped spellings of it that mode allows.
func literalPattern(c byte, mode patternMode) string {
	quoted := regexp.QuoteMeta(string(c))
	if !punct(c) {
		return quoted
	}
	alts := []string{}
	switch mode {
	case modeText:
		alts = append(alts, `\\?`+quoted)
	case modeJSON:
		// A Markdown backslash is itself escaped in JSON.
		alts = append(alts, `(?:\\\\)?`+quoted, fmt.Sprintf(`\\u%04x`, c))
	case modeAny:
		alts = append(alts, `\\?`+quoted, fmt.Sprintf(`\\u%04x`, c))
	}
	if entity, ok := htmlEntities[c]; ok {
		alts = append(alts, regexp.QuoteMeta(entity))
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return "(?:" + strings.Join(alts, "|") + ")"
}

// render writes the placeholder for number n of entity.
func (f *placeholderFormat) render(entity string, n int) string {
	return strings.NewReplacer(entityToken, entity, numberToken, strconv.Itoa(n)).Replace(f.format)
}

// key is the lookup key of a placeholder: its canonical spelling,
// upper-cased, so every variant of it finds the same entry.
func (f *placeholderFormat) key(placeholder string) string {
	return strings.ToUpper(placeholder)
}

// unescapeEntity undoes the escapes an entity type may carry in a match:
// "\u005f" for an underscore in JSON, and Markdown backslashes.
var unescapeEntity = strings.NewReplacer(`\u005f`, "_", `\u005F`, "_", `\U005f`, "_", `\U005F`, "_", `\`, "")

// matchKey is the lookup key of a match of one of the format's patterns.
func (f *placeholderFormat) matchKey(text string, loc []int) string {
	entity, number := text[loc[2]:loc[3]], text[loc[4]:loc[5]]
	if f.numberFirst {
		number, entity = entity, number
	}
	return strings.ToUpper(strings.NewReplacer(entityToken, unescapeEntity.Replace(entity), numberToken, number).Replace(f.format))
}

// mayContain reports whether text may hold a placeholder, cheaply.
func (f *placeholderFormat) mayContain(text string) bool {
	return strings.ContainsAny(text, f.hints)
}
