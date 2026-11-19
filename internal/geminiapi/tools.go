package geminiapi

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// convertTools maps function declarations onto OpenAI function tools. A tool
// with any other member is a Gemini built-in (googleSearch, codeExecution,
// urlContext, …) the gateway cannot run for another provider, so it is
// refused rather than dropped.
func convertTools(tools []Tool) ([]map[string]any, error) {
	var out []map[string]any
	for i, tool := range tools {
		if tool.builtIn != "" {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("tools[%d].%s: built-in tools are not supported on this endpoint; only functionDeclarations translate", i, tool.builtIn), nil,
			).WithParam("tools")
		}
		for j, declaration := range tool.functionDeclarations() {
			if strings.TrimSpace(declaration.Name) == "" {
				return nil, core.NewInvalidRequestError(
					fmt.Sprintf("tools[%d].functionDeclarations[%d].name is required", i, j), nil).WithParam("tools")
			}
			function := map[string]any{"name": declaration.Name}
			if declaration.Description != "" {
				function["description"] = declaration.Description
			}
			parameters, err := declarationParameters(declaration)
			if err != nil {
				return nil, core.NewInvalidRequestError(
					fmt.Sprintf("tools[%d].functionDeclarations[%d]: %v", i, j, err), err).WithParam("tools")
			}
			if parameters != nil {
				function["parameters"] = parameters
			}
			out = append(out, map[string]any{"type": "function", "function": function})
		}
	}
	return out, nil
}

// declarationParameters returns a declaration's parameters as JSON Schema:
// parametersJsonSchema as sent, or parameters converted from Gemini's Schema.
func declarationParameters(declaration FunctionDeclaration) (any, error) {
	if raw := firstRaw(declaration.ParametersJSONSchema, declaration.ParametersJSONSchemaAlt); raw != nil {
		var schema any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("parametersJsonSchema: %v", err)
		}
		return schema, nil
	}
	if raw := firstRaw(declaration.Parameters); raw != nil {
		schema, err := jsonSchemaFromGemini(raw)
		if err != nil {
			return nil, fmt.Errorf("parameters: %v", err)
		}
		return schema, nil
	}
	return nil, nil
}

// firstRaw returns the first value that is present and not JSON null.
func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, raw := range values {
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && !core.IsJSONNull(trimmed) {
			return trimmed
		}
	}
	return nil
}

// convertToolConfig maps functionCallingConfig onto tool_choice. A subset of
// allowed functions becomes an allowed_tools choice, which every provider
// either supports or narrows the tool list for.
func convertToolConfig(cfg *FunctionCallingConfig) any {
	if cfg == nil {
		return nil
	}
	names := cfg.allowedFunctionNames()
	switch strings.ToUpper(strings.TrimSpace(cfg.Mode)) {
	case "NONE":
		return "none"
	case "ANY":
		switch len(names) {
		case 0:
			return "required"
		case 1:
			return map[string]any{"type": "function", "function": map[string]any{"name": names[0]}}
		}
		return allowedTools("required", names)
	case "AUTO", "VALIDATED":
		if len(names) > 0 {
			return allowedTools("auto", names)
		}
		return "auto"
	}
	return nil
}

func allowedTools(mode string, names []string) map[string]any {
	tools := make([]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name}})
	}
	return map[string]any{
		"type":          "allowed_tools",
		"allowed_tools": map[string]any{"mode": mode, "tools": tools},
	}
}
