package anthropicapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

const outputSchema = `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`

const wantResponseFormat = `{"type":"json_schema","json_schema":{"name":"output","strict":true,"schema":` + outputSchema + `}}`

func TestToChatRequestOutputConfig(t *testing.T) {
	tests := []struct {
		name           string
		fields         string
		wantEffort     string // "" means no reasoning
		wantFormat     string // "" means no response_format
		wantInvalidArg bool
	}{
		{name: "effort", fields: `"output_config":{"effort":"max"}`, wantEffort: "max"},
		{name: "effort wins over thinking", fields: `"thinking":{"type":"enabled","budget_tokens":2048},"output_config":{"effort":"high"}`,
			wantEffort: "high"},
		{name: "format", fields: `"output_config":{"format":{"type":"json_schema","schema":` + outputSchema + `}}`,
			wantFormat: wantResponseFormat},
		{name: "deprecated output_format", fields: `"output_format":{"type":"json_schema","schema":` + outputSchema + `}`,
			wantFormat: wantResponseFormat},
		{name: "unsupported format", fields: `"output_config":{"format":{"type":"regex","pattern":"a+"}}`, wantInvalidArg: true},
		{name: "format without schema", fields: `"output_config":{"format":{"type":"json_schema"}}`, wantInvalidArg: true},
		{name: "format not an object", fields: `"output_config":{"format":"json"}`, wantInvalidArg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],`+tt.fields+`}`)
			chat, err := ToChatRequest(req)
			if tt.wantInvalidArg {
				var gwErr *core.GatewayError
				require.ErrorAs(t, err, &gwErr)
				assert.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())

				// Routing decisions must not fail on it: a native Claude route
				// lets Anthropic validate the format itself.
				_, err = ToChatRequestLenient(req)
				require.NoError(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantEffort == "" {
				assert.Nil(t, chat.Reasoning)
			} else {
				require.NotNil(t, chat.Reasoning)
				assert.Equal(t, tt.wantEffort, chat.Reasoning.Effort)
			}
			if tt.wantFormat == "" {
				assert.False(t, chat.ExtraFields.HasAny("response_format"))
			} else {
				assert.JSONEq(t, tt.wantFormat, string(chat.ExtraFields.Lookup("response_format")))
			}
		})
	}
}

func TestStrictCompatible(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   bool
	}{
		{name: "strict object", schema: outputSchema, want: true},
		{name: "missing additionalProperties", schema: `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`},
		{name: "optional property", schema: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"additionalProperties":false}`},
		{name: "loose nested item", schema: `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}}}}},"required":["list"],"additionalProperties":false}`},
		{name: "loose definition", schema: `{"type":"object","properties":{},"additionalProperties":false,"$defs":{"d":{"type":"object","properties":{"x":{"type":"string"}}}}}`},
		{name: "scalar", schema: `{"type":"string"}`, want: true},
		{name: "strict anyOf", schema: `{"anyOf":[{"type":"string"},` + outputSchema + `]}`, want: true},
		{name: "loose anyOf branch", schema: `{"anyOf":[{"type":"string"},{"type":"object","properties":{"x":{"type":"string"}}}]}`},
		{name: "nullable object without additionalProperties", schema: `{"type":["object","null"],"properties":{"a":{"type":"string"}},"required":["a"]}`},
		{name: "strict nullable object", schema: `{"type":["object","null"],"properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`, want: true},
		{name: "allOf", schema: `{"allOf":[` + outputSchema + `]}`},
		{name: "conditional", schema: `{"type":"object","properties":{},"additionalProperties":false,"if":{"type":"object"},"then":{"type":"object"}}`},
		{name: "patternProperties", schema: `{"type":"object","properties":{},"additionalProperties":false,"patternProperties":{"^x":{"type":"string"}}}`},
		{name: "malformed required entries", schema: `{"type":"object","properties":{"a":{"type":"string"}},"required":[{},["a"],1],"additionalProperties":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema any
			require.NoError(t, json.Unmarshal([]byte(tt.schema), &schema))
			assert.Equal(t, tt.want, strictCompatible(schema))
		})
	}
}

// A schema strict mode would reject is sent non-strict instead of failing.
func TestToChatRequestOutputConfigLooseSchema(t *testing.T) {
	for _, loose := range []string{
		`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name"]}`,
		`{"type":["object","null"],"properties":{"name":{"type":"string"}},"required":["name"]}`,
	} {
		chat, err := ToChatRequest(mustDecode(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":`+loose+`}}}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"json_schema","json_schema":{"name":"output","strict":false,"schema":`+loose+`}}`,
			string(chat.ExtraFields.Lookup("response_format")), loose)
	}
}
