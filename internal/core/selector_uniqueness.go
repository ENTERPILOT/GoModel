package core

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// DuplicateSelectorField reports the first top-level "model", "provider", or
// "stream" member that a JSON object body repeats, or "" when each appears at
// most once.
//
// JSON parsers disagree on which duplicate wins: gjson keeps the first
// occurrence, encoding/json and most provider parsers keep the last. A repeated
// selector could therefore be authorized as one model and executed as another,
// so the gateway rejects such bodies instead of picking a side. Keys are
// matched case-insensitively, as encoding/json matches them to struct fields,
// so "Model" repeats "model".
//
// The scan walks only the top-level members and allocates nothing for the
// common case; it never panics on malformed input but reports "" for it, so
// callers validate the body separately.
func DuplicateSelectorField(body []byte) string {
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return ""
	}
	i++
	var modelSeen, providerSeen, streamSeen bool
	for {
		i = skipJSONSpace(body, i)
		if i >= len(body) {
			return ""
		}
		switch body[i] {
		case '}':
			return ""
		case ',':
			i++
			continue
		case '"':
		default:
			return ""
		}

		keyStart := i
		i = skipJSONString(body, i)
		if i < 0 {
			return ""
		}
		name := jsonMemberName(body[keyStart:i])
		var seen *bool
		switch name {
		case "model":
			seen = &modelSeen
		case "provider":
			seen = &providerSeen
		case "stream":
			seen = &streamSeen
		}
		if seen != nil {
			if *seen {
				return name
			}
			*seen = true
		}

		i = skipJSONSpace(body, i)
		if i >= len(body) || body[i] != ':' {
			return ""
		}
		i = skipJSONValue(body, i+1)
		if i < 0 {
			return ""
		}
	}
}

// jsonMemberName returns the selector field a quoted JSON key names, or "".
// Keys without escapes are viewed in place; escaped keys are decoded so
// "mod\u0065l" still counts as "model".
func jsonMemberName(quoted []byte) string {
	raw := quoted[1 : len(quoted)-1]
	if slices.Contains(raw, '\\') {
		return SelectorFieldName(gjson.ParseBytes(quoted).Str)
	}
	// Longer keys cannot fold to a selector name; checking first keeps the
	// conversion below on the stack.
	if len(raw) > selectorFieldMaxLen {
		return ""
	}
	return SelectorFieldName(string(raw))
}

// selectorFieldMaxLen is the longest key, in bytes, that can fold to a
// selector name: "provider", or "stream" spelled with the two-byte long s.
const selectorFieldMaxLen = len("provider") + 1

// SelectorFieldName returns "model", "provider", or "stream" when key names
// that top-level selector, ignoring case as encoding/json does when it
// matches a key to a struct field, or "" otherwise.
func SelectorFieldName(key string) string {
	switch {
	case strings.EqualFold(key, "model"):
		return "model"
	case strings.EqualFold(key, "provider"):
		return "provider"
	case strings.EqualFold(key, "stream"):
		return "stream"
	default:
		return ""
	}
}

func skipJSONSpace(body []byte, i int) int {
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipJSONString returns the index just past the string starting at body[i],
// or -1 when it is unterminated.
func skipJSONString(body []byte, i int) int {
	for i++; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// skipJSONValue returns the index just past the value starting at body[i]
// (after optional whitespace), or -1 when the value is unterminated.
func skipJSONValue(body []byte, i int) int {
	i = skipJSONSpace(body, i)
	if i >= len(body) {
		return -1
	}
	switch body[i] {
	case '"':
		return skipJSONString(body, i)
	case '{', '[':
		depth := 0
		for i < len(body) {
			switch body[i] {
			case '"':
				i = skipJSONString(body, i)
				if i < 0 {
					return -1
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return -1
	default:
		for i < len(body) {
			switch body[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i
			}
			i++
		}
		return i
	}
}

// NewDuplicateSelectorFieldError is the invalid-request error returned for a
// body that repeats a top-level selector field.
func NewDuplicateSelectorFieldError(field string) *GatewayError {
	return NewInvalidRequestError(fmt.Sprintf("duplicate top-level %q field in request body", field), nil)
}
