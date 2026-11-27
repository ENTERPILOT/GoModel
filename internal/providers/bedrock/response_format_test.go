package bedrock

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/enterpilot/gomodel/internal/core"
)

func responseFormatRequest(responseFormat string) *core.ChatRequest {
	return &core.ChatRequest{
		Model: "anthropic.claude-haiku-4-5-20251001-v1:0",
		Messages: []core.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(responseFormat),
		}),
	}
}

func TestBuildConverseParts_ResponseFormatJSONSchema(t *testing.T) {
	const schema = `{"type":"object","properties":{"zeta":{"type":"string"},"alpha":{"type":"string"}},"additionalProperties":false}`
	parts, err := buildConverseParts(responseFormatRequest(
		`{"type":"json_schema","json_schema":{"name":"answer","description":"the answer","strict":true,"schema":` + schema + `}}`))
	require.NoError(t, err)

	for name, config := range map[string]*brtypes.OutputConfig{
		"converse": converseInput(parts).OutputConfig,
		"stream":   converseStreamInput(parts).OutputConfig,
	} {
		require.NotNil(t, config, name)
		require.NotNil(t, config.TextFormat, name)
		assert.Equal(t, brtypes.OutputFormatTypeJsonSchema, config.TextFormat.Type, name)
		structure, ok := config.TextFormat.Structure.(*brtypes.OutputFormatStructureMemberJsonSchema)
		require.True(t, ok, "%s: structure = %T", name, config.TextFormat.Structure)
		got := structure.Value
		assert.Equal(t, awssdk.String(schema), got.Schema, name)
		assert.Equal(t, awssdk.String("answer"), got.Name, name)
		assert.Equal(t, awssdk.String("the answer"), got.Description, name)
	}
}

func TestBuildConverseParts_ResponseFormatWithoutSchema(t *testing.T) {
	for _, responseFormat := range []string{
		`{"type":"json_object"}`,
		`{"type":"json_schema","json_schema":{"name":"answer"}}`,
		`{"type":"json_schema","json_schema":{"name":"answer","schema":{}}}`,
	} {
		parts, err := buildConverseParts(responseFormatRequest(responseFormat))
		require.NoError(t, err, responseFormat)
		assert.Nil(t, converseInput(parts).OutputConfig, responseFormat)
		require.Len(t, parts.system, 2, responseFormat)
		text, ok := parts.system[1].(*brtypes.SystemContentBlockMemberText)
		require.True(t, ok, "%s: got %#v", responseFormat, parts.system[1])
		assert.Contains(t, text.Value, "valid JSON object", responseFormat)
	}
}

func TestBuildConverseParts_ResponseFormatText(t *testing.T) {
	for _, responseFormat := range []string{`null`, `{"type":"text"}`} {
		parts, err := buildConverseParts(responseFormatRequest(responseFormat))
		require.NoError(t, err, responseFormat)
		assert.Nil(t, converseInput(parts).OutputConfig, responseFormat)
		assert.Len(t, parts.system, 1, responseFormat)
	}
}

func TestBuildConverseParts_RejectsUnsupportedResponseFormat(t *testing.T) {
	for responseFormat, want := range map[string]string{
		`"json_object"`:          "response_format must be an object",
		`{}`:                     "unsupported response_format type",
		`{"type":""}`:            "unsupported response_format type",
		`{"type":" text "}`:      "unsupported response_format type",
		`{"type":"xml"}`:         "unsupported response_format type",
		`{"type":"json_schema"}`: "response_format.json_schema is required",
		`{"type":"json_schema","json_schema":{"schema":[1]}}`:  "response_format.json_schema.schema must be an object",
		`{"type":"json_schema","json_schema":{"schema":null}}`: "response_format.json_schema.schema must be an object",
	} {
		_, err := buildConverseParts(responseFormatRequest(responseFormat))
		assert.ErrorContains(t, err, want, responseFormat)
	}
}
