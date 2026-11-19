// Package longspeech lets one text-to-speech request carry more text than the
// upstream accepts in a single call: Split cuts the input into parts at
// sentence boundaries, and Join plays the parts' audio back as one stream.
package longspeech

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// breakKind ranks the places a text can be cut, weakest first. A part ends at
// the last cut of the strongest kind that fits, so speech is interrupted where
// a reader would pause anyway.
type breakKind uint8

const (
	breakNone     breakKind = iota
	breakWord               // after whitespace
	breakClause             // after a comma, semicolon or colon
	breakLine               // after a line break: a heading or list item
	breakSentence           // after a sentence
	breakKinds
)

// The scans around a candidate cut are bounded so that a pathological input
// (a megabyte of closing brackets, say) cannot make splitting quadratic.
const (
	maxClosers      = 8  // closing quotes and brackets after a terminator
	maxAbbreviation = 12 // characters in a word that may be an abbreviation
	maxLookahead    = 16 // whitespace and opening quotes before the next word
)

// abbreviations are words whose trailing period does not end a sentence. The
// list is a sample of titles and shorthand that a capitalized word often
// follows; the lowercase-continuation and dotted-initials rules in
// endsSentence catch most others in any cased script. A missing entry costs
// little: a part may then end after the abbreviation, but only when that is
// the last sentence boundary that fits.
var abbreviations = map[string]bool{
	// English and Latin
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true, "jr": true,
	"st": true, "mt": true, "vs": true, "etc": true, "no": true, "vol": true,
	"fig": true, "inc": true, "ltd": true, "co": true, "corp": true, "gen": true,
	"col": true, "lt": true, "sgt": true, "capt": true, "rev": true, "hon": true,
	"approx": true, "dept": true, "est": true, "ca": true, "cf": true, "al": true,
	"pp": true, "ed": true, "eds": true, "op": true, "viz": true,
	// German, Polish, French, Spanish
	"nr": true, "bzw": true, "ggf": true, "vgl": true, "evtl": true, "inkl": true, "str": true,
	"np": true, "tzn": true, "tzw": true, "godz": true, "ul": true, "mgr": true, "inż": true, "wg": true,
	"mme": true, "mlle": true, "env": true, "sra": true, "srta": true, "ud": true, "uds": true,
}

// Split cuts text into parts of at most limit characters (Unicode code
// points), each ending at a sentence boundary when one fits. A sentence
// longer than limit is cut at a line break, then at clause punctuation, then
// between words, and only as a last resort mid-word. Parts carry no
// surrounding whitespace and are never empty.
//
// A text that already fits, or a limit that is not positive, yields the text
// itself as the only part; blank text yields none.
func Split(text string, limit int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	runes := []rune(text)
	var parts []string
	for start := 0; ; {
		for start < len(runes) && unicode.IsSpace(runes[start]) {
			start++
		}
		if start >= len(runes) {
			return parts
		}
		end := lastRuneOfPart(runes, start, limit) + 1
		parts = append(parts, strings.TrimRightFunc(string(runes[start:end]), unicode.IsSpace))
		start = end
	}
}

// lastRuneOfPart returns the index of the last rune of the part that begins
// at start.
func lastRuneOfPart(runes []rune, start, limit int) int {
	end := start + limit
	if end >= len(runes) {
		return len(runes) - 1
	}
	var last [breakKinds]int
	for i := start; i < end; i++ {
		last[breakAfter(runes, i)] = i + 1 // stored +1 so zero means "none"
	}
	for kind := breakSentence; kind > breakNone; kind-- {
		if last[kind] > 0 {
			return last[kind] - 1
		}
	}
	return hardCut(runes, start, end-1)
}

// breakAfter classifies a cut between runes[i] and the rune that follows.
func breakAfter(runes []rune, i int) breakKind {
	r := runes[i]
	if r == '\n' {
		return breakLine
	}
	if unicode.IsSpace(r) {
		return breakWord
	}
	// Punctuation between two digits belongs to a number: 3.14, 1,000, 12:30.
	if i > 0 && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
		return breakNone
	}
	if endsSentence(runes, i) {
		return breakSentence
	}
	if endsClause(runes, i) {
		return breakClause
	}
	return breakNone
}

// endsSentence reports whether a sentence ends at runes[i]: a terminator, or
// the closing quotes and brackets that follow one.
func endsSentence(runes []rune, i int) bool {
	term := i
	for term > 0 && i-term < maxClosers && isCloser(runes[term]) {
		term--
	}
	if !isTerminator(runes[term]) {
		return false
	}
	if i+1 == len(runes) {
		return true
	}
	next := runes[i+1]
	if isTerminator(next) || isCloser(next) {
		return false // still inside the run: "?!", ".)", "。」"
	}
	if !needsSpaceAfter(runes[term]) {
		return true // 。！？ and their kin end a sentence with no space after
	}
	// Scripts written without spaces between sentences also use ASCII
	// punctuation that way: "你好吗?我很好".
	if !unicode.IsSpace(next) && !unicode.In(next, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return false // 3.14, example.com, v2.0, ?query=
	}
	if runes[term] == '.' && isAbbreviation(runes, term) {
		return false
	}
	return !continuesInLowercase(runes, i+1)
}

// isTerminator reports whether r ends a sentence in some script. Unicode's
// Sentence_Terminal property covers ". ! ?" and their counterparts elsewhere
// (。！？ ։ ؟ ۔ । ። ...); the ellipsis is not in it.
func isTerminator(r rune) bool {
	return r == '…' || unicode.Is(unicode.STerm, r)
}

// needsSpaceAfter reports whether a terminator only ends a sentence when
// whitespace follows it. These also sit inside numbers, hosts, versions and
// abbreviations; the terminators of other scripts are unambiguous.
func needsSpaceAfter(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '．', '﹒':
		return true
	}
	return false
}

// isCloser reports whether r can close a quotation or bracket after a
// terminator: He said "stop." (Really?) 「はい。」 „Halt.“ Any quotation mark
// counts, because which one closes depends on the language.
func isCloser(r rune) bool {
	return r == '"' || r == '\'' || unicode.In(r, unicode.Pe, unicode.Pf, unicode.Pi)
}

// isAbbreviation reports whether the period at runes[dot] ends an
// abbreviation, an initial, or a number rather than a sentence.
func isAbbreviation(runes []rune, dot int) bool {
	begin := dot
	for begin > 0 && !unicode.IsSpace(runes[begin-1]) {
		if begin--; dot-begin > maxAbbreviation {
			return false
		}
	}
	word := strings.TrimLeftFunc(string(runes[begin:dot]), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if word == "" {
		return false
	}
	if strings.IndexFunc(word, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return true // "3. Mai", "1. Introduction"
	}
	if abbreviations[strings.ToLower(word)] {
		return true
	}
	// Initials and dotted shorthand: "J. K. Rowling", "e.g.", "U.S.", "z.B.".
	pieces := strings.Split(word, ".")
	for _, piece := range pieces {
		letters := utf8.RuneCountInString(piece)
		if letters == 0 || letters > 3 || strings.IndexFunc(piece, func(r rune) bool { return !unicode.IsLetter(r) }) >= 0 {
			return false
		}
	}
	if len(pieces) > 1 {
		return true
	}
	initial, _ := utf8.DecodeRuneInString(word)
	return utf8.RuneCountInString(word) == 1 && unicode.IsUpper(initial)
}

// continuesInLowercase reports whether the next word after from starts with a
// lowercase letter, which means the sentence went on: "approx. five",
// "Wait… what", `"Really?" she asked`.
func continuesInLowercase(runes []rune, from int) bool {
	for i := from; i < len(runes) && i-from < maxLookahead; i++ {
		r := runes[i]
		if unicode.IsSpace(r) || r == '"' || r == '\'' || unicode.In(r, unicode.Ps, unicode.Pi) {
			continue
		}
		return unicode.IsLower(r)
	}
	return false
}

// endsClause reports whether runes[i] is clause punctuation a part may end on.
func endsClause(runes []rune, i int) bool {
	switch runes[i] {
	case ',', ';', ':':
		// Without a space these belong to a token: "12:30", "http://", "a,b".
		return i+1 == len(runes) || unicode.IsSpace(runes[i+1])
	case '，', '、', '；', '：', '،', '؛':
		return true
	}
	return false
}

// hardCut picks where to cut a run that has no break at all, moving back from
// cut so a base character keeps its combining marks and joined emoji stay
// whole.
func hardCut(runes []rune, start, cut int) int {
	for i := cut; i > start; i-- {
		if !joinsNext(runes[i], runes[i+1]) {
			return i
		}
	}
	return cut
}

const zeroWidthJoiner = '‍'

// joinsNext reports whether r and the rune after it form one visible character.
func joinsNext(r, next rune) bool {
	return r == zeroWidthJoiner || next == zeroWidthJoiner || unicode.Is(unicode.M, next) ||
		(next >= 0x1F3FB && next <= 0x1F3FF) // emoji skin tone modifiers
}
