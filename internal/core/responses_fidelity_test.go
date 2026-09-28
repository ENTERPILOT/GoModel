package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
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
			if err := json.Unmarshal([]byte(tt.body), &resp); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			for _, want := range tt.want {
				if !bytes.Contains(encoded, []byte(want)) {
					t.Fatalf("response = %s, want %s", encoded, want)
				}
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
			if err := json.Unmarshal([]byte(tt.body), &resp); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if !bytes.Contains(encoded, []byte(tt.want)) {
				t.Fatalf("response = %s, want %s", encoded, tt.want)
			}
			if got := bytes.Count(encoded, []byte(`"incomplete_details"`)); got != 1 {
				t.Fatalf("response = %s, want a single incomplete_details member, got %d", encoded, got)
			}
		})
	}
}

// Typed members stay authoritative: a value set on the struct is emitted once,
// from the field, not from the passthrough object.
func TestResponsesResponseTypedMembersWinOverPassthrough(t *testing.T) {
	var resp ResponsesResponse
	body := `{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-mini","output":[],` +
		`"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	resp.Status = "incomplete"

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Count(encoded, []byte(`"status"`)) != 1 {
		t.Fatalf("response = %s, want a single status member", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"status":"incomplete"`)) {
		t.Fatalf("response = %s, want the typed status", encoded)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v, want the typed usage", resp.Usage)
	}
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
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			err := req.ValidateInput()
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("ValidateInput() = %v, want nil", err)
				}
				return
			}

			var gatewayErr *GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("ValidateInput() = %v, want a gateway error", err)
			}
			if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", gatewayErr.HTTPStatusCode())
			}
			if gatewayErr.Message != "Missing required parameter: 'input'." {
				t.Fatalf("message = %q", gatewayErr.Message)
			}
			if gatewayErr.Param == nil || *gatewayErr.Param != "input" {
				t.Fatalf("param = %v, want input", gatewayErr.Param)
			}
			if gatewayErr.Code == nil || *gatewayErr.Code != "missing_required_parameter" {
				t.Fatalf("code = %v, want missing_required_parameter", gatewayErr.Code)
			}
		})
	}
}
