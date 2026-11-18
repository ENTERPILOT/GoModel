package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloneJSONValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{name: "nil", in: nil, want: nil},
		{name: "string", in: "text", want: "text"},
		{name: "number", in: 1.5, want: 1.5},
		{name: "bool", in: true, want: true},
		{name: "nil map becomes empty map", in: map[string]any(nil), want: map[string]any{}},
		{name: "empty map", in: map[string]any{}, want: map[string]any{}},
		{name: "nil array becomes empty array", in: []any(nil), want: []any{}},
		{name: "nil string slice", in: []string(nil), want: []string(nil)},
		{name: "empty string slice", in: []string{}, want: []string{}},
		{
			name: "nested",
			in: map[string]any{
				"a": []any{map[string]any{"b": []any{1.0, "c"}}, nil},
				"d": []string{"e", "f"},
			},
			want: map[string]any{
				"a": []any{map[string]any{"b": []any{1.0, "c"}}, nil},
				"d": []string{"e", "f"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CloneJSONValue(tt.in))
		})
	}
}

func TestCloneJSONValueDoesNotAliasSource(t *testing.T) {
	src := map[string]any{
		"obj":  map[string]any{"k": "v"},
		"arr":  []any{map[string]any{"k": "v"}, "x"},
		"strs": []string{"a"},
	}

	clone, ok := CloneJSONValue(src).(map[string]any)
	require.True(t, ok)
	obj, ok := clone["obj"].(map[string]any)
	require.True(t, ok)
	arr, ok := clone["arr"].([]any)
	require.True(t, ok)
	inner, ok := arr[0].(map[string]any)
	require.True(t, ok)
	strs, ok := clone["strs"].([]string)
	require.True(t, ok)

	clone["new"] = true
	obj["k"] = "changed"
	inner["k"] = "changed"
	arr[1] = "changed"
	strs[0] = "changed"

	assert.Equal(t, map[string]any{
		"obj":  map[string]any{"k": "v"},
		"arr":  []any{map[string]any{"k": "v"}, "x"},
		"strs": []string{"a"},
	}, src)
}

func TestCloneJSONValueSharesOtherValues(t *testing.T) {
	type point struct{ X int }
	ptr := &point{X: 1}
	assert.Same(t, ptr, CloneJSONValue(ptr))

	bytes := []byte("abc")
	clonedBytes, ok := CloneJSONValue(bytes).([]byte)
	require.True(t, ok)
	assert.Same(t, &bytes[0], &clonedBytes[0])

	raw := json.RawMessage(`{}`)
	clonedRaw, ok := CloneJSONValue(raw).(json.RawMessage)
	require.True(t, ok)
	assert.Same(t, &raw[0], &clonedRaw[0])
}
