package bedrock

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

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
	if err != nil {
		t.Fatalf("buildConverseParts: %v", err)
	}

	for name, config := range map[string]*brtypes.OutputConfig{
		"converse": converseInput(parts).OutputConfig,
		"stream":   converseStreamInput(parts).OutputConfig,
	} {
		if config == nil || config.TextFormat == nil {
			t.Fatalf("%s: OutputConfig.TextFormat is not set", name)
		}
		if config.TextFormat.Type != brtypes.OutputFormatTypeJsonSchema {
			t.Errorf("%s: type = %q", name, config.TextFormat.Type)
		}
		structure, ok := config.TextFormat.Structure.(*brtypes.OutputFormatStructureMemberJsonSchema)
		if !ok {
			t.Fatalf("%s: structure = %T", name, config.TextFormat.Structure)
		}
		got := structure.Value
		if got.Schema == nil || *got.Schema != schema {
			t.Errorf("%s: schema = %v, want %s", name, got.Schema, schema)
		}
		if got.Name == nil || *got.Name != "answer" {
			t.Errorf("%s: name = %v", name, got.Name)
		}
		if got.Description == nil || *got.Description != "the answer" {
			t.Errorf("%s: description = %v", name, got.Description)
		}
	}
}

func TestBuildConverseParts_ResponseFormatWithoutSchema(t *testing.T) {
	for _, responseFormat := range []string{
		`{"type":"json_object"}`,
		`{"type":"json_schema","json_schema":{"name":"answer"}}`,
		`{"type":"json_schema","json_schema":{"name":"answer","schema":{}}}`,
	} {
		parts, err := buildConverseParts(responseFormatRequest(responseFormat))
		if err != nil {
			t.Fatalf("%s: %v", responseFormat, err)
		}
		if converseInput(parts).OutputConfig != nil {
			t.Errorf("%s: OutputConfig should not be set", responseFormat)
		}
		if len(parts.system) != 2 {
			t.Fatalf("%s: system blocks = %d, want 2", responseFormat, len(parts.system))
		}
		text, ok := parts.system[1].(*brtypes.SystemContentBlockMemberText)
		if !ok || !strings.Contains(text.Value, "valid JSON object") {
			t.Errorf("%s: missing JSON instruction, got %#v", responseFormat, parts.system[1])
		}
	}
}

func TestBuildConverseParts_ResponseFormatText(t *testing.T) {
	for _, responseFormat := range []string{`null`, `{"type":"text"}`} {
		parts, err := buildConverseParts(responseFormatRequest(responseFormat))
		if err != nil {
			t.Fatalf("%s: %v", responseFormat, err)
		}
		if converseInput(parts).OutputConfig != nil || len(parts.system) != 1 {
			t.Errorf("%s: request should be unchanged", responseFormat)
		}
	}
}

func TestBuildConverseParts_RejectsUnsupportedResponseFormat(t *testing.T) {
	for responseFormat, want := range map[string]string{
		`"json_object"`:          "response_format must be an object",
		`{"type":"xml"}`:         "unsupported response_format type",
		`{"type":"json_schema"}`: "response_format.json_schema is required",
		`{"type":"json_schema","json_schema":{"schema":[1]}}`: "response_format.json_schema.schema must be an object",
	} {
		_, err := buildConverseParts(responseFormatRequest(responseFormat))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", responseFormat, err, want)
		}
	}
}
