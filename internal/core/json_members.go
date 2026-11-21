package core

import (
	"bytes"
	"errors"
	"strings"
	"unsafe"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// The chat types decode their large nested members (choices, messages,
// message content) by calling the member's decoder on a sub-slice of their
// own input. Left to json.Unmarshal, each nested UnmarshalJSON receives a fresh
// copy of its bytes, so a long message content was copied once per level.
//
// The helpers below locate members the way the decoder matches struct fields
// (case-insensitively, the last occurrence deciding the value) and hand out
// sub-slices of the input. Callees must not retain those bytes: every decoder
// they reach copies what it keeps.

// errUnexpectedJSONKind reports a member whose value has a JSON kind the field
// cannot hold. It only triggers the reference decode, which reports the error
// the decoder has always returned.
var errUnexpectedJSONKind = errors.New("unexpected JSON kind")

// forEachMatchingJSONMember calls visit with the value of every member of the
// JSON object data whose key matches name as the decoder matches a field name,
// in document order. data must be valid JSON; a value that is not an object
// has no members.
func forEachMatchingJSONMember(data []byte, name string, visit func(value []byte) error) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var err error
	parseJSONInPlace(trimmed).ForEach(func(key, value gjson.Result) bool {
		if !strings.EqualFold(key.Str, name) {
			return true
		}
		err = visit(jsonResultBytes(value))
		return err == nil
	})
	return err
}

// lastMatchingJSONMember returns the value of the member that decides a
// json.RawMessage field named name: the last matching one, or nil when absent.
func lastMatchingJSONMember(data []byte, name string) []byte {
	var last []byte
	_ = forEachMatchingJSONMember(data, name, func(value []byte) error {
		last = value
		return nil
	})
	return last
}

// decodeJSONObjectSliceMember decodes the slice member name of the JSON object
// data. Every occurrence is decoded, as the decoder does, and the last decides
// the value.
func decodeJSONObjectSliceMember[T any, P interface {
	*T
	json.Unmarshaler
}](data []byte, name string) ([]T, error) {
	var items []T
	err := forEachMatchingJSONMember(data, name, func(value []byte) error {
		var err error
		items, err = decodeJSONObjectSlice[T, P](value)
		return err
	})
	return items, err
}

// decodeJSONObjectSlice decodes a slice member as the decoder does: null
// yields nil, an array one element per value (an empty array a non-nil empty
// slice), each element through its own UnmarshalJSON.
func decodeJSONObjectSlice[T any, P interface {
	*T
	json.Unmarshaler
}](value []byte) ([]T, error) {
	switch value[0] {
	case 'n':
		return nil, nil
	case '[':
	default:
		return nil, errUnexpectedJSONKind
	}
	array := parseJSONInPlace(value)
	count := 0
	array.ForEach(func(_, _ gjson.Result) bool {
		count++
		return true
	})
	items := make([]T, count)
	var err error
	i := 0
	array.ForEach(func(_, element gjson.Result) bool {
		err = P(&items[i]).UnmarshalJSON(jsonResultBytes(element))
		i++
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// parseJSONInPlace parses b without gjson.ParseBytes' copy of it. Results
// alias b and must not outlive it.
func parseJSONInPlace(b []byte) gjson.Result {
	return gjson.Parse(unsafe.String(unsafe.SliceData(b), len(b)))
}

// jsonResultBytes returns the raw bytes of a value parsed by parseJSONInPlace,
// which are a sub-slice of the parsed input.
func jsonResultBytes(value gjson.Result) []byte {
	return unsafe.Slice(unsafe.StringData(value.Raw), len(value.Raw))
}
