package bedrock

import (
	"bytes"

	"github.com/goccy/go-json"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/enterpilot/gomodel/internal/core"
)

// jsonObjectInstruction emulates response_format {"type":"json_object"}: Converse
// structured output only accepts a JSON schema. The wording matches the
// Anthropic adapter, which has the same limitation.
const jsonObjectInstruction = "Your entire response must be a single valid JSON object. " +
	`The first character of your response must be "{" and the last must be "}". ` +
	"Do not use backticks or a markdown code block anywhere in the response."

// openAIResponseFormat is the OpenAI-compatible chat response_format field.
type openAIResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
	} `json:"json_schema"`
}

// applyResponseFormat maps response_format onto the Converse request.
// json_schema becomes native structured output (outputConfig.textFormat);
// json_object becomes a system-prompt instruction.
func applyResponseFormat(parts *converseParts, extraFields core.UnknownJSONFields) error {
	raw := bytes.TrimSpace(extraFields.Lookup("response_format"))
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var responseFormat openAIResponseFormat
	if err := json.Unmarshal(raw, &responseFormat); err != nil {
		return core.NewInvalidRequestError("response_format must be an object", err)
	}

	switch responseFormat.Type {
	case "text":
		return nil
	case "json_object":
		appendJSONObjectInstruction(parts)
		return nil
	case "json_schema":
		spec := responseFormat.JSONSchema
		if spec == nil {
			return core.NewInvalidRequestError("response_format.json_schema is required for json_schema", nil)
		}
		var schema map[string]any
		if bytes.Equal(spec.Schema, []byte("null")) {
			return core.NewInvalidRequestError("response_format.json_schema.schema must be an object", nil)
		}
		if len(spec.Schema) > 0 {
			if err := json.Unmarshal(spec.Schema, &schema); err != nil {
				return core.NewInvalidRequestError("response_format.json_schema.schema must be an object", err)
			}
		}
		if len(schema) == 0 {
			// Nothing to constrain; still ask for JSON.
			appendJSONObjectInstruction(parts)
			return nil
		}
		// The caller's bytes are sent as-is to keep the property order.
		definition := brtypes.JsonSchemaDefinition{Schema: awssdk.String(string(spec.Schema))}
		if spec.Name != "" {
			definition.Name = awssdk.String(spec.Name)
		}
		if spec.Description != "" {
			definition.Description = awssdk.String(spec.Description)
		}
		parts.outputCfg = &brtypes.OutputConfig{TextFormat: &brtypes.OutputFormat{
			Type:      brtypes.OutputFormatTypeJsonSchema,
			Structure: &brtypes.OutputFormatStructureMemberJsonSchema{Value: definition},
		}}
		return nil
	default:
		return core.NewInvalidRequestError("unsupported response_format type: "+responseFormat.Type, nil)
	}
}

func appendJSONObjectInstruction(parts *converseParts) {
	parts.system = append(parts.system, &brtypes.SystemContentBlockMemberText{Value: jsonObjectInstruction})
}
