package geminiapi

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"
)

// jsonSchemaFromGemini converts a Gemini Schema (function parameters,
// responseSchema) into JSON Schema. Gemini's Schema is an OpenAPI 3.0 subset
// that differs from JSON Schema in ways the SDKs rely on: type names are
// upper-case enum values ("OBJECT", "STRING"), a nullable value is marked
// with nullable: true instead of a "null" type, propertyOrdering has no JSON
// Schema counterpart, and keywords may arrive in snake_case (the Python SDK
// sends property_ordering). Providers that validate schemas strictly reject
// each of these, so they are normalized; every other keyword is already JSON
// Schema and is kept as it is.
func jsonSchemaFromGemini(raw json.RawMessage) (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return nil, fmt.Errorf("must be a schema object")
	}
	convertSchema(schema)
	return schema, nil
}

// snakeSchemaKeywords maps the snake_case spelling of a Schema keyword onto
// its JSON Schema name. Property names are client data and are never renamed:
// only the keywords of a schema object are.
var snakeSchemaKeywords = map[string]string{
	"any_of":            "anyOf",
	"max_items":         "maxItems",
	"min_items":         "minItems",
	"max_length":        "maxLength",
	"min_length":        "minLength",
	"max_properties":    "maxProperties",
	"min_properties":    "minProperties",
	"property_ordering": "propertyOrdering",
}

func convertSchema(schema map[string]any) {
	for snake, camel := range snakeSchemaKeywords {
		if value, ok := schema[snake]; ok {
			delete(schema, snake)
			if _, exists := schema[camel]; !exists {
				schema[camel] = value
			}
		}
	}
	delete(schema, "propertyOrdering")
	nullable, _ := schema["nullable"].(bool)
	delete(schema, "nullable")
	if schemaType, ok := schema["type"].(string); ok {
		schemaType = strings.ToLower(schemaType)
		if nullable && schemaType != "null" {
			schema["type"] = []any{schemaType, "null"}
		} else {
			schema["type"] = schemaType
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, property := range properties {
			convertSubschema(property)
		}
	}
	convertSubschema(schema["items"])
	if anyOf, ok := schema["anyOf"].([]any); ok {
		for _, option := range anyOf {
			convertSubschema(option)
		}
	}
}

func convertSubschema(value any) {
	if schema, ok := value.(map[string]any); ok {
		convertSchema(schema)
	}
}
