package core

import (
	"slices"
	"strings"
)

// AllowedToolsChoice reads a Chat Completions allowed_tools tool_choice,
// {"type": "allowed_tools", "allowed_tools": {"mode": ..., "tools": [...]}}.
// ok is false for any other choice. names lists the function names in order;
// required reports mode "required" (any other mode, or none, means auto).
func AllowedToolsChoice(choice any) (names []string, required, ok bool) {
	choiceMap, isMap := choice.(map[string]any)
	if !isMap {
		return nil, false, false
	}
	if choiceType, _ := choiceMap["type"].(string); strings.TrimSpace(choiceType) != "allowed_tools" {
		return nil, false, false
	}
	spec, _ := choiceMap["allowed_tools"].(map[string]any)
	tools, _ := spec["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	mode, _ := spec["mode"].(string)
	return names, strings.TrimSpace(mode) == "required", true
}

// NarrowToAllowedTools expresses an allowed_tools choice for a provider that
// cannot limit a turn to a subset of the declared tools: it keeps only the
// allowed functions and turns the choice into "required" or "auto". Any other
// choice is returned unchanged. A subset that names no declared function is
// rejected rather than widened to every declared tool.
func NarrowToAllowedTools(tools []map[string]any, choice any) ([]map[string]any, any, error) {
	names, required, ok := AllowedToolsChoice(choice)
	if !ok {
		return tools, choice, nil
	}
	allowed := make([]map[string]any, 0, len(names))
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if name, _ := fn["name"].(string); slices.Contains(names, name) {
			allowed = append(allowed, tool)
		}
	}
	if len(allowed) == 0 {
		return nil, nil, NewInvalidRequestError("tool_choice.allowed_tools.tools must list at least one declared function", nil)
	}
	if required {
		return allowed, "required", nil
	}
	return allowed, "auto", nil
}
