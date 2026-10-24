package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every Response member the gateway does not model itself must survive a
// decode/encode round trip: OpenAI echoes the whole request back on the
// Response object and clients read those members back.
func TestResponsesResponseRoundTripsUnknownMembers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "request echo members",
			body: `{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-mini",` +
				`"output":[],"instructions":"be terse","metadata":{"k":"v"},"tool_choice":"auto",` +
				`"parallel_tool_calls":false,"temperature":1,"top_p":1,"store":true,` +
				`"text":{"format":{"type":"text"}},"reasoning":{"effort":null},"truncation":"disabled",` +
				`"tools":[],"max_output_tokens":32}`,
			want: []string{
				`"instructions":"be terse"`, `"metadata":{"k":"v"}`, `"tool_choice":"auto"`,
				`"parallel_tool_calls":false`, `"temperature":1`, `"top_p":1`, `"store":true`,
				`"text":{"format":{"type":"text"}}`, `"reasoning":{"effort":null}`,
				`"truncation":"disabled"`, `"tools":[]`, `"max_output_tokens":32`,
			},
		},
		{
			name: "members added after this release",
			body: `{"id":"resp_2","object":"response","status":"completed","output":[],` +
				`"billing":{"payer":"developer"},"x_big":9007199254740993}`,
			want: []string{`"billing":{"payer":"developer"}`, `"x_big":9007199254740993`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp ResponsesResponse
			require.NoError(t, json.Unmarshal([]byte(tt.body), &resp))
			encoded, err := json.Marshal(resp)
			require.NoError(t, err)
			for _, want := range tt.want {
				assert.Contains(t, string(encoded), want)
			}
		})
	}
}

// incomplete_details is sent on every OpenAI Response object, as an explicit
// null on a completed one. It must survive the round trip exactly once,
// whether or not the struct types it.
func TestResponsesResponseKeepsIncompleteDetails(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "explicit null incomplete_details survives",
			body: `{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-mini",` +
				`"output":[],"incomplete_details":null}`,
			want: `"incomplete_details":null`,
		},
		{
			name: "populated incomplete_details is emitted once",
			body: `{"id":"resp_2","object":"response","status":"incomplete","model":"gpt-4o-mini",` +
				`"output":[],"incomplete_details":{"reason":"max_output_tokens"}}`,
			want: `"incomplete_details":{"reason":"max_output_tokens"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp ResponsesResponse
			require.NoError(t, json.Unmarshal([]byte(tt.body), &resp))
			encoded, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), tt.want)
			assert.Equal(t, 1, bytes.Count(encoded, []byte(`"incomplete_details"`)),
				"response = %s, want a single incomplete_details member", encoded)
		})
	}
}

// Typed members stay authoritative: a value set on the struct is emitted once,
// from the field, not from the passthrough object.
func TestResponsesResponseTypedMembersWinOverPassthrough(t *testing.T) {
	var resp ResponsesResponse
	body := `{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-mini","output":[],` +
		`"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	resp.Status = "incomplete"

	encoded, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Equal(t, 1, bytes.Count(encoded, []byte(`"status"`)), "response = %s, want a single status member", encoded)
	assert.Contains(t, string(encoded), `"status":"incomplete"`)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 3, resp.Usage.TotalTokens)
}

// A request with nothing to send is answered locally, in OpenAI's shape,
// instead of reaching a provider as an empty object.
func TestResponsesRequestValidateInput(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "missing input", body: `{"model":"openai/gpt-4o-mini"}`, wantErr: true},
		{name: "null input", body: `{"model":"openai/gpt-4o-mini","input":null}`, wantErr: true},
		{name: "string input", body: `{"model":"openai/gpt-4o-mini","input":"hi"}`},
		{name: "empty string input", body: `{"model":"openai/gpt-4o-mini","input":""}`},
		{name: "array input", body: `{"model":"openai/gpt-4o-mini","input":[]}`},
		{name: "prompt template supplies the input", body: `{"model":"openai/gpt-4o-mini","prompt":{"id":"pmpt_1"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(tt.body), &req))

			err := req.ValidateInput()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			var gatewayErr *GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Equal(t, "Missing required parameter: 'input'.", gatewayErr.Message)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, "input", *gatewayErr.Param)
			require.NotNil(t, gatewayErr.Code)
			assert.Equal(t, "missing_required_parameter", *gatewayErr.Code)
		})
	}
}
