package presidio

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

// argStrings decodes tool-call arguments and returns their string values in
// document order (object keys sorted), so each can be analyzed on its own
// and put back with [withArgStrings]. Arguments that are not a JSON object
// or array yield nothing.
func argStrings(args json.RawMessage) (any, []string, bool) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber() // large integers survive the round trip
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, false
	}
	switch v.(type) {
	case map[string]any, []any:
		return v, collectStrings(v, nil), true
	}
	return nil, nil, false
}

func collectStrings(v any, out []string) []string {
	switch t := v.(type) {
	case string:
		return append(out, t)
	case []any:
		for _, item := range t {
			out = collectStrings(item, out)
		}
	case map[string]any:
		for _, key := range sortedKeys(t) {
			out = collectStrings(t[key], out)
		}
	}
	return out
}

// withArgStrings returns the arguments with the string values replaced, in
// the order [argStrings] listed them.
func withArgStrings(v any, values []string) (json.RawMessage, error) {
	i := 0
	next := func() string {
		s := values[i]
		i++
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(replaceStrings(v, next)); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(buf.Bytes())), nil
}

func replaceStrings(v any, next func() string) any {
	switch t := v.(type) {
	case string:
		return next()
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = replaceStrings(item, next)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, key := range sortedKeys(t) {
			out[key] = replaceStrings(t[key], next)
		}
		return out
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// reserveArgs reserves the placeholder-shaped text of tool-call arguments:
// every decoded string, object keys included, or the raw text when the
// arguments are not JSON. It covers what analysis does not read: keys,
// scalar arguments, and the arguments of roles left out of analysis.
// Decoding matters because JSON may escape "<" as "\u003c".
func reserveArgs(m *mapping, args json.RawMessage) {
	var v any
	if json.Unmarshal(args, &v) != nil {
		m.reserve(string(args))
		return
	}
	reserveTree(m, v)
}

func reserveTree(m *mapping, v any) {
	switch t := v.(type) {
	case string:
		m.reserve(t)
	case []any:
		for _, item := range t {
			reserveTree(m, item)
		}
	case map[string]any:
		for key, item := range t {
			m.reserve(key)
			reserveTree(m, item)
		}
	}
}
