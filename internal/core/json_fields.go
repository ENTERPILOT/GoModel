package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
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

// MergeUnknownJSONFields combines multiple unknown-field objects into one.
// Later field sets override earlier ones for duplicate keys.
func MergeUnknownJSONFields(fields ...UnknownJSONFields) UnknownJSONFields {
	if len(fields) == 0 {
		return UnknownJSONFields{}
	}

	var merged map[string]json.RawMessage
	for _, fieldSet := range fields {
		if fieldSet.IsEmpty() {
			continue
		}

		dec := json.NewDecoder(bytes.NewReader(fieldSet.raw))
		tok, err := dec.Token()
		if err != nil {
			continue
		}
		delim, ok := tok.(json.Delim)
		if !ok || delim != '{' {
			continue
		}

		for dec.More() {
			key, ok := readJSONObjectKey(dec)
			if !ok {
				return UnknownJSONFields{}
			}

			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return UnknownJSONFields{}
			}

			if merged == nil {
				merged = make(map[string]json.RawMessage)
			}
			merged[key] = CloneRawJSON(value)
		}
	}

	return UnknownJSONFieldsFromMap(merged)
}

// UnknownJSONFieldsFromMap converts a raw field map into a compact JSON object.
func UnknownJSONFieldsFromMap(fields map[string]json.RawMessage) UnknownJSONFields {
	if len(fields) == 0 {
		return UnknownJSONFields{}
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
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
		rawValue := CloneRawJSON(fields[key])
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
		fieldName, ok := readJSONObjectKey(dec)
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
	return extractUnknownJSONFieldsObjectByScan(data, knownFields...)
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

func readJSONObjectKey(dec *json.Decoder) (string, bool) {
	keyToken, err := dec.Token()
	if err != nil {
		return "", false
	}
	key, ok := keyToken.(string)
	return key, ok
}

func extractUnknownJSONFieldsObjectByScan(data []byte, knownFields ...string) (UnknownJSONFields, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return UnknownJSONFields{}, fmt.Errorf("expected JSON object")
	}

	known := make(map[string]struct{}, len(knownFields))
	for _, field := range knownFields {
		known[field] = struct{}{}
	}

	buf := bytes.NewBuffer(make([]byte, 0, len(data)))
	buf.WriteByte('{')
	wrote := false

	i := skipJSONWhitespace(data, 1)
	for i < len(data) {
		if data[i] == '}' {
			break
		}

		keyStart := i
		keyEnd, err := scanJSONString(data, keyStart)
		if err != nil {
			return UnknownJSONFields{}, err
		}
		key, err := decodeJSONString(data[keyStart:keyEnd])
		if err != nil {
			return UnknownJSONFields{}, err
		}

		i = skipJSONWhitespace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return UnknownJSONFields{}, fmt.Errorf("expected ':' after object key")
		}
		i = skipJSONWhitespace(data, i+1)

		valueStart := i
		valueEnd, err := scanJSONValue(data, valueStart)
		if err != nil {
			return UnknownJSONFields{}, err
		}

		if _, isKnown := known[key]; !isKnown {
			if wrote {
				buf.WriteByte(',')
			}
			buf.Write(data[keyStart:keyEnd])
			buf.WriteByte(':')
			buf.Write(data[valueStart:valueEnd])
			wrote = true
		}

		i = skipJSONWhitespace(data, valueEnd)
		if i >= len(data) {
			return UnknownJSONFields{}, fmt.Errorf("unterminated JSON object")
		}
		switch data[i] {
		case ',':
			i = skipJSONWhitespace(data, i+1)
			if i >= len(data) {
				return UnknownJSONFields{}, fmt.Errorf("unterminated JSON object")
			}
			if data[i] == '}' {
				return UnknownJSONFields{}, fmt.Errorf("unexpected trailing comma in JSON object")
			}
		case '}':
			// The next loop iteration will terminate cleanly on the closing brace.
		default:
			return UnknownJSONFields{}, fmt.Errorf("expected ',' or '}' after object value")
		}
	}

	if !wrote {
		return UnknownJSONFields{}, nil
	}
	buf.WriteByte('}')
	return UnknownJSONFields{raw: buf.Bytes()}, nil
}

func scanJSONValue(data []byte, start int) (int, error) {
	if start >= len(data) {
		return 0, fmt.Errorf("expected JSON value")
	}
	switch data[start] {
	case '"':
		return scanJSONString(data, start)
	case '{':
		return scanJSONObject(data, start)
	case '[':
		return scanJSONArray(data, start)
	default:
		i := start
		for i < len(data) {
			switch data[i] {
			case ',', '}', ']':
				goto validateLiteral
			case ' ', '\n', '\r', '\t':
				goto validateLiteral
			}
			i++
		}
	validateLiteral:
		if i == start {
			return 0, fmt.Errorf("expected JSON literal")
		}
		if err := validateJSONLiteral(data[start:i]); err != nil {
			return 0, err
		}
		return i, nil
	}
}

func scanJSONObject(data []byte, start int) (int, error) {
	i := start + 1
	for i < len(data) {
		i = skipJSONWhitespace(data, i)
		if i >= len(data) {
			break
		}
		if data[i] == '}' {
			return i + 1, nil
		}
		keyEnd, err := scanJSONString(data, i)
		if err != nil {
			return 0, err
		}
		i = skipJSONWhitespace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return 0, fmt.Errorf("expected ':' after object key")
		}
		i = skipJSONWhitespace(data, i+1)
		valueEnd, err := scanJSONValue(data, i)
		if err != nil {
			return 0, err
		}
		i = skipJSONWhitespace(data, valueEnd)
		if i >= len(data) {
			return 0, fmt.Errorf("unterminated JSON object")
		}
		switch data[i] {
		case ',':
			i = skipJSONWhitespace(data, i+1)
			if i >= len(data) {
				return 0, fmt.Errorf("unterminated JSON object")
			}
			if data[i] == '}' {
				return 0, fmt.Errorf("unexpected trailing comma in JSON object")
			}
		case '}':
			return i + 1, nil
		default:
			return 0, fmt.Errorf("expected ',' or '}' after object value")
		}
	}
	return 0, fmt.Errorf("unterminated JSON object")
}

func scanJSONArray(data []byte, start int) (int, error) {
	i := start + 1
	for i < len(data) {
		i = skipJSONWhitespace(data, i)
		if i >= len(data) {
			break
		}
		if data[i] == ']' {
			return i + 1, nil
		}
		valueEnd, err := scanJSONValue(data, i)
		if err != nil {
			return 0, err
		}
		i = skipJSONWhitespace(data, valueEnd)
		if i >= len(data) {
			return 0, fmt.Errorf("unterminated JSON array")
		}
		switch data[i] {
		case ',':
			i = skipJSONWhitespace(data, i+1)
			if i >= len(data) {
				return 0, fmt.Errorf("unterminated JSON array")
			}
			if data[i] == ']' {
				return 0, fmt.Errorf("unexpected trailing comma in JSON array")
			}
		case ']':
			return i + 1, nil
		default:
			return 0, fmt.Errorf("expected ',' or ']' after array element")
		}
	}
	return 0, fmt.Errorf("unterminated JSON array")
}

func validateJSONLiteral(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid JSON literal: %w", err)
	}
	switch value.(type) {
	case nil, bool, float64:
		return nil
	default:
		return fmt.Errorf("invalid JSON literal")
	}
}

func scanJSONString(data []byte, start int) (int, error) {
	if start >= len(data) || data[start] != '"' {
		return 0, fmt.Errorf("expected JSON string")
	}
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated JSON string")
}

func decodeJSONString(raw []byte) (string, error) {
	if len(raw) < 2 {
		return "", fmt.Errorf("invalid JSON string")
	}
	if bytes.IndexByte(raw, '\\') == -1 {
		return string(raw[1 : len(raw)-1]), nil
	}
	return strconv.Unquote(string(raw))
}

func skipJSONWhitespace(data []byte, start int) int {
	i := start
	for i < len(data) {
		switch data[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}
