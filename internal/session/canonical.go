package session

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// canonicalSegment gives semantically equivalent JSON the same session
// anchor. In particular, object-key order, insignificant whitespace, and
// equivalent string escapes must not split one conversation into multiple
// auto-detected sessions. Number spelling/precision is preserved while arrays
// retain their original order.
//
// The output is byte-identical to Marshal of the segment decoded into `any`
// with UseNumber (pinned by TestCanonicalSegmentMatchesStdlib and the
// randomized TestCanonicalSegmentMatchesOracle), so auto-detected ids stay
// stable across implementations. The common path walks the already
// tokenized gjson tree and appends straight into one buffer instead of
// decoding into maps and re-encoding; inputs the walker cannot reproduce
// exactly fall back to that decode/encode path.
func canonicalSegment(result gjson.Result) json.RawMessage {
	if !result.Exists() || len(result.Raw) == 0 {
		return nil
	}
	if gjson.Valid(result.Raw) {
		buf := make([]byte, 0, len(result.Raw)+8)
		if buf, ok := appendCanonical(buf, result); ok {
			return buf
		}
	}
	return canonicalSegmentDecoded(rawSegment(result))
}

// rawSegment clones a gjson result's raw JSON. gjson results alias the parsed
// body, so the copy keeps the anchor independent of the request buffer.
func rawSegment(result gjson.Result) json.RawMessage {
	if !result.Exists() {
		return nil
	}
	return json.RawMessage(strings.Clone(result.Raw))
}

// canonicalSegmentDecoded is the decode/encode reference path. The
// trailing-data guard must decode to io.EOF rather than check Decoder.More:
// More treats a stray closing bracket ("1]", "1}") as end of input, which
// would canonicalize malformed raw segments instead of falling back to their
// exact bytes.
func canonicalSegmentDecoded(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}

// appendCanonical appends the canonical encoding of a valid gjson value. It
// reports false when the value contains a string the walker cannot decode
// exactly like encoding/json (see canonicalStringOK), in which case the
// caller must use the reference path.
func appendCanonical(buf []byte, r gjson.Result) ([]byte, bool) {
	switch r.Type {
	case gjson.Null:
		return append(buf, "null"...), true
	case gjson.False:
		return append(buf, "false"...), true
	case gjson.True:
		return append(buf, "true"...), true
	case gjson.Number:
		return append(buf, strings.TrimSpace(r.Raw)...), true
	case gjson.String:
		if !canonicalStringOK(r) {
			return buf, false
		}
		return appendJSONString(buf, r.Str), true
	}
	if r.IsArray() {
		buf = append(buf, '[')
		first := true
		ok := true
		r.ForEach(func(_, value gjson.Result) bool {
			if !first {
				buf = append(buf, ',')
			}
			first = false
			buf, ok = appendCanonical(buf, value)
			return ok
		})
		if !ok {
			return buf, false
		}
		return append(buf, ']'), true
	}
	if r.IsObject() {
		return appendCanonicalObject(buf, r)
	}
	return buf, false
}

type canonicalMember struct {
	key   string
	value gjson.Result
}

// appendCanonicalObject writes members sorted by decoded key, as encoding/json
// does for maps. A duplicated key keeps its last value, matching map decoding.
func appendCanonicalObject(buf []byte, r gjson.Result) ([]byte, bool) {
	var members []canonicalMember
	ok := true
	r.ForEach(func(key, value gjson.Result) bool {
		if !canonicalStringOK(key) {
			ok = false
			return false
		}
		members = append(members, canonicalMember{key: key.Str, value: value})
		return true
	})
	if !ok {
		return buf, false
	}
	slices.SortStableFunc(members, func(a, b canonicalMember) int {
		return strings.Compare(a.key, b.key)
	})
	buf = append(buf, '{')
	written := 0
	for i, member := range members {
		if i+1 < len(members) && members[i+1].key == member.key {
			continue
		}
		if written > 0 {
			buf = append(buf, ',')
		}
		written++
		buf = appendJSONString(buf, member.key)
		buf = append(buf, ':')
		buf, ok = appendCanonical(buf, member.value)
		if !ok {
			return buf, false
		}
	}
	return append(buf, '}'), true
}

// canonicalStringOK reports whether gjson's decoded form of a string token is
// guaranteed to equal encoding/json's. The two diverge only on malformed
// input: raw control characters (gjson tolerates them, encoding/json rejects
// the document), invalid UTF-8 (encoding/json substitutes U+FFFD per byte)
// and \u escapes in the surrogate range (unpaired or mismatched surrogates
// are consumed differently). All are left to the reference decoder.
func canonicalStringOK(r gjson.Result) bool {
	if !utf8.ValidString(r.Str) {
		return false
	}
	raw := r.Raw
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 {
			return false
		}
	}
	for {
		i := strings.Index(raw, `\u`)
		if i < 0 {
			return true
		}
		raw = raw[i+2:]
		if len(raw) >= 4 && (raw[0] == 'd' || raw[0] == 'D') && raw[1] >= '8' {
			return false
		}
	}
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string exactly as goccy's Marshal does
// with HTML escaping on: \n, \r and \t use their short escapes, other control
// characters (including \b and \f, unlike encoding/json), '<', '>', '&' and
// U+2028/U+2029 are \u-escaped, and everything else is written verbatim.
func appendJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			buf = append(buf, s[start:i]...)
			switch c {
			case '\\', '"':
				buf = append(buf, '\\', c)
			case '\n':
				buf = append(buf, '\\', 'n')
			case '\r':
				buf = append(buf, '\\', 'r')
			case '\t':
				buf = append(buf, '\\', 't')
			default:
				buf = append(buf, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		rn, size := utf8.DecodeRuneInString(s[i:])
		if rn == '\u2028' || rn == '\u2029' {
			buf = append(buf, s[start:i]...)
			buf = append(buf, '\\', 'u', '2', '0', '2', hexDigits[rn&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	buf = append(buf, s[start:]...)
	return append(buf, '"')
}
