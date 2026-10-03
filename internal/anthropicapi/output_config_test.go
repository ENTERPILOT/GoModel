package anthropicapi

import (
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
