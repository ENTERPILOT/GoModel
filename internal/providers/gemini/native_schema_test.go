package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Len(t, tools[0].FunctionDeclarations, 1)

	declaration := tools[0].FunctionDeclarations[0]
	require.Empty(t, declaration.Parameters)
	require.NotEmpty(t, declaration.ParametersJSONSchema)
	_, ok := parameters["$schema"]
	require.True(t, ok)

	var schema map[string]any
	err = json.Unmarshal(declaration.ParametersJSONSchema, &schema)
	require.NoError(t, err)
	assert.NotContains(t, schema, "$schema", "parametersJsonSchema = %s, want $schema stripped", declaration.ParametersJSONSchema)
	assert.Equal(t, false, schema["additionalProperties"])

	properties, _ := schema["properties"].(map[string]any)
	labels, _ := properties["labels"].(map[string]any)
	additionalProperties, _ := labels["additionalProperties"].(map[string]any)
	assert.Equal(t, "string", additionalProperties["type"])
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
	require.NoError(t, err)

	var schema map[string]any
	err = json.Unmarshal(tools[0].FunctionDeclarations[0].ParametersJSONSchema, &schema)
	require.NoError(t, err)
	assert.Equal(t, "object", schema["type"])
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
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestGeminiToolsFromOpenAIRejectsUnsupportedToolShapes(t *testing.T) {
	tests := []struct {
		name      string
		tool      map[string]any
		wantError string
	}{
		{
			name:      "hosted tool type",
			tool:      map[string]any{"type": "web_search_preview"},
			wantError: "unsupported tool type: web_search_preview",
		},
		{
			name:      "missing function object",
			tool:      map[string]any{"type": "function"},
			wantError: "tool.function must be an object",
		},
		{
			name:      "empty function name",
			tool:      map[string]any{"type": "function", "function": map[string]any{"name": " "}},
			wantError: "tool.function.name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := geminiToolsFromOpenAI([]map[string]any{tt.tool})
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestCopyResponseFormatJSONSchemaUsesResponseJsonSchema(t *testing.T) {
	raw := []byte(`{
		"type": "json_schema",
		"json_schema": {
			"name": "planets",
			"schema": {
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {"planets": {"type": "array", "items": {"type": "string"}}},
				"required": ["planets"],
				"additionalProperties": false
			}
		}
	}`)

	cfg := map[string]any{}
	copyResponseFormat(raw, cfg)
	assert.Equal(t, "application/json", cfg["responseMimeType"])
	assert.NotContains(t, cfg, "responseSchema")

	schema, ok := cfg["responseJsonSchema"].(map[string]any)
	require.True(t, ok, "responseJsonSchema = %#v, want object", cfg["responseJsonSchema"])
	assert.NotContains(t, schema, "$schema")
	assert.Equal(t, false, schema["additionalProperties"])
}

func TestCopyResponseFormatJSONObject(t *testing.T) {
	cfg := map[string]any{}
	copyResponseFormat([]byte(`{"type": "json_object"}`), cfg)
	assert.Equal(t, "application/json", cfg["responseMimeType"])
	assert.NotContains(t, cfg, "responseJsonSchema")
}
