package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/tidwall/gjson"
)

// UnknownJSONFields stores unknown JSON object members as a single raw object.
// This avoids allocating a map for every decoded chat-family request while
// still allowing lookups and round-trip preservation when needed.
type UnknownJSONFields struct {
	raw json.RawMessage
}

// CloneRawJSON returns a detached copy of a raw JSON value.
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

func extractUnknownJSONFields(data []byte, knownFields ...string) (UnknownJSONFields, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return UnknownJSONFields{}, fmt.Errorf("expected JSON object")
	}
	if !gjson.ValidBytes(data) {
		return UnknownJSONFields{}, fmt.Errorf("invalid JSON object")
	}

	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return UnknownJSONFields{}, fmt.Errorf("expected JSON object")
	}

	buf := bytes.NewBuffer(make([]byte, 0, len(data)))
	buf.WriteByte('{')
	wrote := false
	root.ForEach(func(key, value gjson.Result) bool {
		if containsJSONField(knownFields, key.String()) {
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

func containsJSONField(knownFields []string, field string) bool {
	for _, known := range knownFields {
		if field == known {
			return true
		}
	}
	return false
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
