package presidio

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode/utf8"
)

// span is one detected entity with byte offsets into the text it was found
// in. The analyzer reports character (code point) offsets, which
// [byteSpans] converts.
type span struct {
	entity string
	start  int
	end    int
	score  float64
}

// byteSpans converts analyzer results (code-point offsets) to byte spans,
// drops results outside the text or with an empty range, and resolves
// overlaps: of two overlapping results the higher score wins, then the
// longer one. The result is sorted by start.
func byteSpans(text string, results []analyzerResult) []span {
	offsets := runeOffsets(text)
	byteAt := func(i int) int {
		if i == len(offsets) {
			return len(text)
		}
		return offsets[i]
	}
	spans := make([]span, 0, len(results))
	for _, r := range results {
		if r.Start < 0 || r.End > len(offsets) || r.Start >= r.End || strings.TrimSpace(r.EntityType) == "" {
			continue
		}
		spans = append(spans, span{entity: r.EntityType, start: byteAt(r.Start), end: byteAt(r.End), score: r.Score})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].score != spans[j].score {
			return spans[i].score > spans[j].score
		}
		if li, lj := spans[i].end-spans[i].start, spans[j].end-spans[j].start; li != lj {
			return li > lj
		}
		return spans[i].start < spans[j].start
	})
	var kept []span
	for _, s := range spans {
		overlaps := false
		for _, k := range kept {
			if s.start < k.end && k.start < s.end {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].start < kept[j].start })
	return kept
}

// runeOffsets returns the byte offset of every rune of text, so offsets[i]
// is where code point i starts. It is sized by the rune count: sized by
// len(text), a mostly multi-byte text would reserve several times the
// offsets it needs.
func runeOffsets(text string) []int {
	offsets := make([]int, 0, utf8.RuneCountInString(text))
	for i := range text {
		offsets = append(offsets, i)
	}
	return offsets
}

// runeBytes returns the byte offset of the first n runes of text (len(text)
// when text has fewer).
func runeBytes(text string, n int) int {
	offset := 0
	for i := 0; i < n && offset < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[offset:])
		offset += size
	}
	return offset
}

// rewrite replaces every span of text with the string replacement returns
// for it, in order. Spans must be sorted and non-overlapping.
func rewrite(text string, spans []span, replacement func(span, string) string) string {
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, s := range spans {
		b.WriteString(text[last:s.start])
		b.WriteString(replacement(s, text[s.start:s.end]))
		last = s.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// Operator values for the "operator" config key: how a detected value is
// rewritten.
const (
	OperatorReplace = "replace"
	OperatorMask    = "mask"
	OperatorRedact  = "redact"
	OperatorHash    = "hash"
)

// maskChar is what mask writes in place of every character.
const maskChar = "*"

// staticReplacement renders the operators that need no request state.
// replace is handled by [mapping] because its placeholders are numbered.
func staticReplacement(operator string, value string) string {
	switch operator {
	case OperatorMask:
		return strings.Repeat(maskChar, utf8.RuneCountInString(value))
	case OperatorRedact:
		return ""
	case OperatorHash:
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	return value
}
