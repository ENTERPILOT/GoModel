package core

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/goccy/go-json"

	"github.com/tidwall/gjson"
)

// UnknownJSONFields stores unknown JSON object members as a single raw object.
// This avoids allocating a map for every decoded chat-family request while
// still allowing lookups and round-trip preservation when needed.
type UnknownJSONFields struct {
	raw json.RawMessage
}

// CloneRawJSON returns a detached copy of a raw JSON value.
// IsJSONNull reports whether trimmed JSON data is empty or the null literal.
func IsJSONNull(trimmed []byte) bool {
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func CloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// CloneUnknownJSONFields returns a detached copy of a raw unknown-field object.
func CloneUnknownJSONFields(fields UnknownJSONFields) UnknownJSONFields {
	return UnknownJSONFields{raw: CloneRawJSON(fields.raw)}
}

// UnknownJSONFieldsFromMap converts a raw field map into a compact JSON object.
func UnknownJSONFieldsFromMap(fields map[string]json.RawMessage) UnknownJSONFields {
	return unknownJSONFieldsFromMap(fields, true)
}

func unknownJSONFieldsFromMap(fields map[string]json.RawMessage, cloneValues bool) UnknownJSONFields {
	if len(fields) == 0 {
		return UnknownJSONFields{}
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	buf := bytes.NewBuffer(make([]byte, 0, len(keys)*16))
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyBody, err := json.Marshal(key)
		if err != nil {
			panic(fmt.Sprintf("core: marshal unknown JSON field key %q: %v", key, err))
		}
		buf.Write(keyBody)
		buf.WriteByte(':')
		rawValue := fields[key]
		if cloneValues {
			rawValue = CloneRawJSON(rawValue)
		}
		if len(rawValue) == 0 {
			buf.WriteString("null")
			continue
		}
		buf.Write(rawValue)
	}
	buf.WriteByte('}')
	return UnknownJSONFields{raw: buf.Bytes()}
}

// MergeUnknownJSONFields returns base with the given raw members added; additions
// override existing members on key conflict. It lets translation layers inject
// derived fields (such as a chat response_format mapped from a Responses text
// format) into a request's passthrough object without a dedicated typed field.
func MergeUnknownJSONFields(base UnknownJSONFields, additions map[string]json.RawMessage) (UnknownJSONFields, error) {
	if len(additions) == 0 {
		return base, nil
	}
	additionFields := UnknownJSONFieldsFromMap(additions)
	if err := validateUnknownJSONObject(additionFields.raw); err != nil {
		return UnknownJSONFields{}, err
	}
	if base.IsEmpty() {
		return additionFields, nil
	}

	overrideKeys := make(map[string]struct{}, len(additions))
	for key := range additions {
		overrideKeys[key] = struct{}{}
	}

	merged, err := mergeUnknownJSONFieldsRaw(base.raw, additionFields.raw, overrideKeys)
	if err != nil {
		return UnknownJSONFields{}, err
	}
	return UnknownJSONFields{raw: merged}, nil
}

func mergeUnknownJSONFieldsRaw(baseBody, additionBody []byte, overrideKeys map[string]struct{}) ([]byte, error) {
	baseBody = bytes.TrimSpace(baseBody)
	additionBody = bytes.TrimSpace(additionBody)
	if len(additionBody) == 0 || bytes.Equal(additionBody, []byte("{}")) {
		return CloneRawJSON(baseBody), nil
	}
	if len(baseBody) == 0 || bytes.Equal(baseBody, []byte("{}")) {
		return CloneRawJSON(additionBody), nil
	}

	totalCap, err := mergedJSONObjectCap(len(baseBody), len(additionBody))
	if err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(make([]byte, 0, totalCap))
	buf.WriteByte('{')
	wrote := false
	if err := appendUnknownJSONMembers(buf, baseBody, overrideKeys, &wrote); err != nil {
		return nil, err
	}
	if err := appendUnknownJSONMembers(buf, additionBody, nil, &wrote); err != nil {
		return nil, err
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func validateUnknownJSONObject(body []byte) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || bytes.Equal(body, []byte("{}")) {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("invalid JSON object")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return fmt.Errorf("expected JSON object")
	}
	return nil
}

func appendUnknownJSONMembers(buf *bytes.Buffer, body []byte, skip map[string]struct{}, wrote *bool) error {
	body = bytes.TrimSpace(body)
	if err := validateUnknownJSONObject(body); err != nil {
		return err
	}
	if len(body) == 0 || bytes.Equal(body, []byte("{}")) {
		return nil
	}
	root := gjson.ParseBytes(body)

	root.ForEach(func(key, value gjson.Result) bool {
		if _, shouldSkip := skip[key.String()]; shouldSkip {
			return true
		}
		if *wrote {
			buf.WriteByte(',')
		}
		buf.WriteString(key.Raw)
		buf.WriteByte(':')
		buf.WriteString(value.Raw)
		*wrote = true
		return true
	})
	return nil
}

// Lookup returns the raw JSON value for key or nil when absent.
// It scans the stored object on demand so single-lookups stay allocation-light,
// but repeated lookups on the same value are linear in the raw JSON size.
func (fields UnknownJSONFields) Lookup(key string) json.RawMessage {
	if len(fields.raw) == 0 {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(fields.raw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}

	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil
		}
		fieldName, ok := keyToken.(string)
		if !ok {
			return nil
		}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil
		}
		if fieldName == key {
			return CloneRawJSON(value)
		}
	}

	return nil
}

// IsEmpty reports whether the container has no stored fields.
func (fields UnknownJSONFields) IsEmpty() bool {
	trimmed := bytes.TrimSpace(fields.raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}"))
}

// extractUnknownJSONFields captures the object's keys that are not in
// knownFields, preserving their raw bytes for passthrough (Postel's Law).
//
// Precondition: data must already be valid JSON. Every caller is an
// UnmarshalJSON method that calls json.Unmarshal on the same bytes first, so a
// separate gjson.ValidBytes walk here would re-scan the whole document for no
// benefit. The cheap first-byte and IsObject checks remain to reject non-object
// JSON explicitly.
func extractUnknownJSONFields(data []byte, knownFields ...string) (UnknownJSONFields, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return UnknownJSONFields{}, fmt.Errorf("expected JSON object")
	}

	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return UnknownJSONFields{}, fmt.Errorf("expected JSON object")
	}

	buf := bytes.NewBuffer(make([]byte, 0, len(data)))
	buf.WriteByte('{')
	wrote := false
	root.ForEach(func(key, value gjson.Result) bool {
		if slices.Contains(knownFields, key.String()) {
			return true
		}
		if wrote {
			buf.WriteByte(',')
		}
		buf.WriteString(key.Raw)
		buf.WriteByte(':')
		buf.WriteString(value.Raw)
		wrote = true
		return true
	})
	if !wrote {
		return UnknownJSONFields{}, nil
	}

	buf.WriteByte('}')
	return UnknownJSONFields{raw: buf.Bytes()}, nil
}

func marshalWithUnknownJSONFields(base any, extraFields UnknownJSONFields) ([]byte, error) {
	baseBody, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if extraFields.IsEmpty() {
		return baseBody, nil
	}
	return mergeUnknownJSONObject(baseBody, extraFields.raw)
}

func mergeUnknownJSONObject(baseBody, extraBody []byte) ([]byte, error) {
	baseBody = bytes.TrimSpace(baseBody)
	extraBody = bytes.TrimSpace(extraBody)
	if len(extraBody) == 0 || bytes.Equal(extraBody, []byte("{}")) {
		return CloneRawJSON(baseBody), nil
	}
	if len(baseBody) == 0 {
		return nil, fmt.Errorf("base JSON object is empty")
	}
	if baseBody[0] != '{' || baseBody[len(baseBody)-1] != '}' {
		return nil, fmt.Errorf("base JSON is not an object")
	}
	if extraBody[0] != '{' || extraBody[len(extraBody)-1] != '}' {
		return nil, fmt.Errorf("unknown JSON fields are not an object")
	}
	if bytes.Equal(baseBody, []byte("{}")) {
		return CloneRawJSON(extraBody), nil
	}

	totalCap, err := mergedJSONObjectCap(len(baseBody), len(extraBody))
	if err != nil {
		return nil, err
	}
	merged := make([]byte, 0, totalCap)
	merged = append(merged, baseBody[:len(baseBody)-1]...)
	if !bytes.Equal(extraBody, []byte("{}")) {
		merged = append(merged, ',')
		merged = append(merged, extraBody[1:]...)
	}
	return merged, nil
}

func mergedJSONObjectCap(baseLen, extraLen int) (int, error) {
	if extraLen <= 0 {
		return 0, fmt.Errorf("unknown JSON fields are empty")
	}
	if baseLen > math.MaxInt-(extraLen-1) {
		return 0, fmt.Errorf("combined JSON object too large")
	}
	return baseLen + extraLen - 1, nil
}
