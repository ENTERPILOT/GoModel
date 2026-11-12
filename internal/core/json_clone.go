package core

import "slices"

// CloneJSONValue deep-copies a decoded JSON value: objects (map[string]any)
// and arrays ([]any) are copied recursively, and []string, which Go-built
// configs use for string arrays, is copied too, keeping nil and empty apart. Every other value, including
// scalars, structs, pointers and other slice types, is returned as is and
// shared with the source.
func CloneJSONValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = CloneJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = CloneJSONValue(item)
		}
		return out
	case []string:
		return slices.Clone(value)
	default:
		return v
	}
}
