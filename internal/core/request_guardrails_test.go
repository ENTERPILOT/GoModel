package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTakeRequestGuardrails(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantNames []string
		wantRest  string
	}{
		{"absent", `{"model":"m","stop":["x"]}`, nil, `{"stop":["x"]}`},
		{"names", `{"model":"m","guardrails":["pii","judge"],"stop":["x"]}`, []string{"pii", "judge"}, `{"stop":["x"]}`},
		{"only member", `{"model":"m","guardrails":["pii"]}`, []string{"pii"}, `{}`},
		{"trimmed and deduplicated", `{"model":"m","guardrails":[" pii ","pii","judge"]}`, []string{"pii", "judge"}, `{}`},
		{"empty list", `{"model":"m","guardrails":[]}`, []string{}, `{}`},
		{"null", `{"model":"m","guardrails":null}`, nil, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := DecodeChatRequest([]byte(tt.body), nil)
			require.NoError(t, err)

			names, rest, err := TakeRequestGuardrails(req.ExtraFields)

			require.NoError(t, err)
			assert.Equal(t, tt.wantNames, names)
			assert.Nil(t, rest.Lookup(RequestGuardrailsField), "the field must not reach a provider")
			req.ExtraFields = rest
			encoded, err := req.MarshalJSON()
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "guardrails")
			if tt.wantRest != `{}` {
				assert.JSONEq(t, `["x"]`, string(rest.Lookup("stop")), "other unknown fields are kept")
			}
		})
	}
}

func TestTakeRequestGuardrails_RejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"string", `{"model":"m","guardrails":"pii"}`, "must be an array of guardrail names"},
		{"object", `{"model":"m","guardrails":{"config_id":"x"}}`, "must be an array of guardrail names"},
		{"non-string entry", `{"model":"m","guardrails":["pii",1]}`, "must be an array of guardrail names"},
		{"object entry", `{"model":"m","guardrails":[{"pii":{}}]}`, "must be an array of guardrail names"},
		{"empty name", `{"model":"m","guardrails":["pii"," "]}`, "must not contain an empty name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := DecodeChatRequest([]byte(tt.body), nil)
			require.NoError(t, err)

			_, _, err = TakeRequestGuardrails(req.ExtraFields)

			var gatewayErr *GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Equal(t, ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, "guardrails", *gatewayErr.Param)
		})
	}
}

func TestNewRequestGuardrailUnavailableError(t *testing.T) {
	err := NewRequestGuardrailUnavailableError("pii")

	assert.Equal(t, http.StatusBadRequest, err.HTTPStatusCode())
	assert.Equal(t, `guardrail "pii" cannot be selected for this request`, err.Message)
	require.NotNil(t, err.Code)
	assert.Equal(t, "guardrail_not_available", *err.Code)
	require.NotNil(t, err.Param)
	assert.Equal(t, "guardrails", *err.Param)
}
