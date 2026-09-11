package core

import (
	"bytes"
	// encoding/json rather than goccy: the field locator walks the body with
	// the standard decoder's token stream and InputOffset.
	stdjson "encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/tidwall/gjson"
)

// jsonDecodeError carries a nested decode failure out of an UnmarshalJSON
// method unchanged. goccy/go-json rebuilds the errors it recognizes when they
// cross a custom unmarshaler, dropping the position that names which member
// failed; an error type it does not recognize is returned as-is, so the
// original travels inside this wrapper.
type jsonDecodeError struct{ err error }

func (e *jsonDecodeError) Error() string { return e.err.Error() }

func (e *jsonDecodeError) Unwrap() error { return e.err }

// wrapJSONDecodeError preserves a decode error raised inside a request type's
// UnmarshalJSON, so NewInvalidRequestBodyError can still locate the member.
func wrapJSONDecodeError(err error) error {
	if err == nil {
		return nil
	}
	return &jsonDecodeError{err: err}
}

// jsonMemberDecodeError marks a decode failure raised while decoding a single
// member's value on its own. Positions inside such an error are relative to
// that fragment rather than to the request body, so the member is named from
// the decode site instead of located in the document.
type jsonMemberDecodeError struct {
	member string
	err    error
}

func (e *jsonMemberDecodeError) Error() string { return e.err.Error() }

func (e *jsonMemberDecodeError) Unwrap() error { return e.err }

// wrapJSONMemberDecodeError attributes a fragment decode failure to the member
// whose value was being decoded.
func wrapJSONMemberDecodeError(member string, err error) error {
	if err == nil {
		return nil
	}
	return &jsonMemberDecodeError{member: member, err: err}
}

// NewInvalidRequestBodyError renders a request-body decode failure as a clean
// client error. Decoder errors are written for Go authors: they name Go types
// and struct fields, and goccy/go-json reports a type mismatch inside a
// complete document as "unexpected end of JSON input", which sends users
// hunting for a truncation that does not exist. This translates the failure
// into the offending JSON member and the type it should have, and sets that
// member as the error param so OpenAI-shaped clients can read it.
func NewInvalidRequestBodyError(body []byte, err error) *GatewayError {
	detail, param := describeJSONDecodeError(body, err)
	gatewayErr := NewInvalidRequestError("invalid request body: "+detail, err)
	if param != "" {
		return gatewayErr.WithParam(param)
	}
	return gatewayErr
}

// describeJSONDecodeError returns the client-facing description of a decode
// failure and, when the failure can be attributed to one JSON member, its
// path (e.g. "messages[0].role").
func describeJSONDecodeError(body []byte, err error) (detail, param string) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "request body is empty", ""
	}
	if !json.Valid(trimmed) {
		return describeInvalidJSON(trimmed, err), ""
	}
	if trimmed[0] != '{' {
		return "request body must be a JSON object", ""
	}

	// A member decoded on its own reports positions inside its own fragment,
	// so the member it was decoded for is used instead of a location.
	if memberErr, ok := errors.AsType[*jsonMemberDecodeError](err); ok {
		if _, wanted := decodeMismatch(memberErr.err); wanted != "" {
			return memberErr.member + ": must be " + wanted, memberErr.member
		}
		return memberErr.member + ": " + fallbackDecodeDetail(memberErr.err), memberErr.member
	}

	offset, wanted := decodeMismatch(err)
	if wanted == "" {
		return fallbackDecodeDetail(err), ""
	}
	path, lookup := jsonMemberAtOffset(trimmed, offset)
	if path == "" || !memberValueConflicts(trimmed, lookup, wanted) {
		return "a field has the wrong type: expected " + wanted, ""
	}
	return path + ": must be " + wanted, path
}

// describeInvalidJSON describes a body that is not valid JSON at all, without
// quoting the decoder's internal wording.
func describeInvalidJSON(trimmed []byte, err error) string {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || !endsJSONDocument(trimmed) {
		return "request body is not valid JSON: the document ends unexpectedly"
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) && syntaxErr.Offset > 0 {
		return "request body is not valid JSON at byte " + strconv.FormatInt(syntaxErr.Offset, 10)
	}
	return "request body is not valid JSON"
}

// endsJSONDocument reports whether the body at least closes its outermost
// container, which separates a truncated document from a malformed one.
func endsJSONDocument(trimmed []byte) bool {
	switch trimmed[0] {
	case '{':
		return trimmed[len(trimmed)-1] == '}'
	case '[':
		return trimmed[len(trimmed)-1] == ']'
	default:
		return true
	}
}

// decodeMismatch extracts the position and the expected type of a type
// mismatch from a decoder error, or wanted == "" when the error is not one.
// goccy/go-json reports mismatches either as an UnmarshalTypeError or, for
// several kinds, as a SyntaxError whose message carries the expected kind and
// whose offset points at the offending value.
func decodeMismatch(err error) (offset int64, wanted string) {
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return typeErr.Offset, describeGoType(typeErr.Type)
	}
	if stdTypeErr, ok := errors.AsType[*stdjson.UnmarshalTypeError](err); ok {
		return stdTypeErr.Offset, describeGoType(stdTypeErr.Type)
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return syntaxErr.Offset, describeDecoderKind(syntaxErr.Error())
	}
	return 0, ""
}

// describeDecoderKind maps goccy's kind-prefixed mismatch messages
// ("json: slice unexpected end of JSON input", "expected { character for map
// value") onto a client-facing type name.
func describeDecoderKind(message string) string {
	if kind, ok := strings.CutSuffix(strings.TrimPrefix(message, "json: "), " unexpected end of JSON input"); ok {
		switch {
		case kind == "slice" || kind == "array":
			return "an array"
		case kind == "bool":
			return "a boolean"
		case kind == "string":
			return "a string"
		case kind == "map" || kind == "struct":
			return "an object"
		case strings.HasPrefix(kind, "number(integer)") || kind == "int" || kind == "uint":
			return "an integer"
		case kind == "float" || strings.HasPrefix(kind, "number"):
			return "a number"
		}
		return ""
	}
	if strings.HasPrefix(message, "expected { character for map value") {
		return "an object"
	}
	return ""
}

// describeGoType names the JSON type a Go type decodes from.
func describeGoType(t reflect.Type) string {
	if t == nil {
		return ""
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	case reflect.Pointer, reflect.Interface:
		return ""
	default:
		return ""
	}
}

// fallbackDecodeDetail keeps a decoder message the translation does not
// recognize readable: the "json: " prefix is dropped and nothing else is
// invented.
func fallbackDecodeDetail(err error) string {
	if err == nil {
		return "request body could not be decoded"
	}
	return strings.TrimPrefix(err.Error(), "json: ")
}

// jsonContainer is one open object or array while walking a JSON document:
// an object tracks the member currently being read, an array its index.
type jsonContainer struct {
	key     string
	index   int
	isArray bool
	wantKey bool
}

// jsonMemberAtOffset returns the path of the JSON member whose value spans
// offset (e.g. "messages[0].role") plus the gjson lookup path for that member,
// or "" when the position cannot be attributed to one member. The decoders
// report the mismatching value's position but not its member, so the document
// is walked to recover it.
func jsonMemberAtOffset(body []byte, offset int64) (path, lookup string) {
	if offset <= 0 || offset > int64(len(body)) {
		return "", ""
	}
	var stack []jsonContainer
	dec := stdjson.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", ""
		}
		top := len(stack) - 1
		isKey := false
		if top >= 0 && stack[top].wantKey {
			if key, ok := tok.(string); ok {
				stack[top].key = key
				stack[top].wantKey = false
				isKey = true
			}
		}
		// goccy reports the offending value's first byte and encoding/json the
		// byte after its last, so the member is the one whose token stream
		// first reaches the offset. The check runs before a container is
		// pushed, so a value that is itself an object or array is attributed
		// to the member holding it.
		if !isKey && dec.InputOffset() >= offset {
			return jsonMemberPath(stack)
		}
		switch {
		case isJSONOpenDelim(tok):
			if delim, _ := tok.(stdjson.Delim); delim == '{' {
				stack = append(stack, jsonContainer{wantKey: true})
			} else {
				stack = append(stack, jsonContainer{isArray: true})
			}
		case isJSONCloseDelim(tok):
			if top < 0 {
				return "", ""
			}
			stack = stack[:top]
			endJSONValue(stack)
		case !isKey:
			endJSONValue(stack)
		}
	}
}

// memberValueConflicts reports whether the member found at a decoder position
// really holds a value of the wrong type. A decoder that reports a position
// inside a fragment it decoded on its own would otherwise put the blame on an
// innocent member, so a location that does not conflict is discarded.
func memberValueConflicts(body []byte, lookup, wanted string) bool {
	value := gjson.GetBytes(body, lookup)
	if !value.Exists() {
		return false
	}
	switch wanted {
	case "an array":
		return !value.IsArray()
	case "an object":
		return !value.IsObject()
	case "a string":
		return value.Type != gjson.String
	case "an integer", "a number":
		return value.Type != gjson.Number
	case "a boolean":
		return value.Type != gjson.True && value.Type != gjson.False
	default:
		return false
	}
}

func isJSONOpenDelim(tok stdjson.Token) bool {
	delim, ok := tok.(stdjson.Delim)
	return ok && (delim == '{' || delim == '[')
}

func isJSONCloseDelim(tok stdjson.Token) bool {
	delim, ok := tok.(stdjson.Delim)
	return ok && (delim == '}' || delim == ']')
}

// endJSONValue records that the innermost container finished a value: an
// object expects a key next, an array advances to the next element.
func endJSONValue(stack []jsonContainer) {
	top := len(stack) - 1
	if top < 0 {
		return
	}
	if stack[top].isArray {
		stack[top].index++
		return
	}
	stack[top].wantKey = true
}

// jsonMemberPath renders the open containers as a member path for the client
// ("messages[0].role") and as a gjson lookup path ("messages.0.role").
func jsonMemberPath(stack []jsonContainer) (path, lookup string) {
	var display, gjsonPath strings.Builder
	for _, container := range stack {
		index := strconv.Itoa(container.index)
		if container.isArray {
			display.WriteString("[" + index + "]")
			if gjsonPath.Len() > 0 {
				gjsonPath.WriteString(".")
			}
			gjsonPath.WriteString(index)
			continue
		}
		if container.key == "" {
			continue
		}
		if display.Len() > 0 {
			display.WriteString(".")
		}
		display.WriteString(container.key)
		if gjsonPath.Len() > 0 {
			gjsonPath.WriteString(".")
		}
		gjsonPath.WriteString(escapeGJSONKey(container.key))
	}
	return display.String(), gjsonPath.String()
}

// escapeGJSONKey escapes the characters gjson treats as path syntax so a
// member whose name contains them is still looked up literally.
func escapeGJSONKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		if strings.ContainsRune(`.*?\|#@`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
