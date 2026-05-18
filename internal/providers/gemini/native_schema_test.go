package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeminiToolsFromOpenAIUsesParametersJSONSchema(t *testing.T) {
	parameters := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
			"labels": map[string]any{
				"type": "object",
				"additionalProperties": map[string]any{
					"type": "string",
				},
			},
		},
		"required": []any{"city"},
	}

	tools, err := geminiToolsFromOpenAI([]map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup_weather",
			"description": "Look up weather",
			"parameters":  parameters,
		},
	}})
	if err != nil {
		t.Fatalf("geminiToolsFromOpenAI() error = %v", err)
	}
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v, want one function declaration", tools)
	}

	declaration := tools[0].FunctionDeclarations[0]
	if len(declaration.Parameters) != 0 {
		t.Fatalf("parameters = %s, want omitted when parametersJsonSchema is used", declaration.Parameters)
	}
	if len(declaration.ParametersJSONSchema) == 0 {
		t.Fatal("parametersJsonSchema is empty")
	}
	if _, ok := parameters["$schema"]; !ok {
		t.Fatal("input parameters were mutated")
	}

	var schema map[string]any
	if err := json.Unmarshal(declaration.ParametersJSONSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal parametersJsonSchema: %v", err)
	}
	if _, ok := schema["$schema"]; ok {
		t.Fatalf("parametersJsonSchema = %s, want $schema stripped", declaration.ParametersJSONSchema)
	}
	if got := schema["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %#v, want false", got)
	}

	properties := schema["properties"].(map[string]any)
	labels := properties["labels"].(map[string]any)
	additionalProperties := labels["additionalProperties"].(map[string]any)
	if got := additionalProperties["type"]; got != "string" {
		t.Fatalf("nested additionalProperties.type = %#v, want string", got)
	}
}

func TestGeminiToolsFromOpenAINormalizesMissingObjectType(t *testing.T) {
	tools, err := geminiToolsFromOpenAI([]map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "lookup_weather",
			"parameters": map[string]any{
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": []any{"city"},
			},
		},
	}})
	if err != nil {
		t.Fatalf("geminiToolsFromOpenAI() error = %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(tools[0].FunctionDeclarations[0].ParametersJSONSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal parametersJsonSchema: %v", err)
	}
	if got := schema["type"]; got != "object" {
		t.Fatalf("type = %#v, want object", got)
	}
}

func TestGeminiToolsFromOpenAIRejectsInvalidParameterSchemas(t *testing.T) {
	tests := []struct {
		name       string
		parameters any
		wantError  string
	}{
		{
			name:       "array parameters",
			parameters: []any{"invalid"},
			wantError:  "tool.function.parameters must be an object",
		},
		{
			name:       "string parameters",
			parameters: "invalid",
			wantError:  "tool.function.parameters must be an object",
		},
		{
			name:       "boolean parameters",
			parameters: true,
			wantError:  "tool.function.parameters must be an object",
		},
		{
			name:       "non object schema type",
			parameters: map[string]any{"type": "array"},
			wantError:  "tool.function.parameters must define an object schema",
		},
		{
			name:       "null schema type",
			parameters: map[string]any{"type": nil},
			wantError:  "tool.function.parameters must define an object schema",
		},
		{
			name:       "empty schema type",
			parameters: map[string]any{"type": ""},
			wantError:  "tool.function.parameters must define an object schema",
		},
		{
			name:       "ambiguous schema type",
			parameters: map[string]any{"type": []any{"object", "null"}},
			wantError:  "tool.function.parameters must define an object schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := geminiToolsFromOpenAI([]map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":       "lookup_weather",
					"parameters": tt.parameters,
				},
			}})
			if err == nil {
				t.Fatal("geminiToolsFromOpenAI() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantError)
			}
		})
	}
}
