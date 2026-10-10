package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowedToolsChoice(t *testing.T) {
	choice := map[string]any{
		"type": "allowed_tools",
		"allowed_tools": map[string]any{
			"mode": "required",
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "tool_a"}},
				map[string]any{"type": "function", "function": map[string]any{"name": " "}},
				map[string]any{"type": "function", "function": map[string]any{"name": "tool_b"}},
			},
		},
	}
	names, required, ok := AllowedToolsChoice(choice)
	assert.True(t, ok)
	assert.True(t, required)
	assert.Equal(t, []string{"tool_a", "tool_b"}, names)

	choice["allowed_tools"].(map[string]any)["mode"] = "auto"
	_, required, ok = AllowedToolsChoice(choice)
	assert.True(t, ok)
	assert.False(t, required)

	for _, other := range []any{nil, "required", map[string]any{"type": "function"}} {
		_, _, ok := AllowedToolsChoice(other)
		assert.False(t, ok, "%#v", other)
	}
}

func TestNarrowToAllowedTools(t *testing.T) {
	tool := func(name string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	}
	tools := []map[string]any{tool("tool_a"), tool("tool_b"), tool("tool_c")}
	allowed := func(mode string, names ...string) map[string]any {
		entries := make([]any, 0, len(names))
		for _, name := range names {
			entries = append(entries, map[string]any{"type": "function", "function": map[string]any{"name": name}})
		}
		return map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{"mode": mode, "tools": entries}}
	}

	narrowed, choice, err := NarrowToAllowedTools(tools, allowed("required", "tool_c", "tool_a"))
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{tool("tool_a"), tool("tool_c")}, narrowed)
	assert.Equal(t, "required", choice)

	narrowed, choice, err = NarrowToAllowedTools(tools, allowed("auto", "tool_b"))
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{tool("tool_b")}, narrowed)
	assert.Equal(t, "auto", choice)

	_, _, err = NarrowToAllowedTools(tools, allowed("required", "unknown"))
	require.ErrorContains(t, err, "tool_choice.allowed_tools.tools")

	narrowed, choice, err = NarrowToAllowedTools(tools, "required")
	require.NoError(t, err)
	assert.Equal(t, tools, narrowed)
	assert.Equal(t, "required", choice)
}
