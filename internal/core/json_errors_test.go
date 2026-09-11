package core

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

type decodeTestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type decodeTestRequest struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	Messages  []decodeTestMessage    `json:"messages"`
	Stream    bool                   `json:"stream"`
	Metadata  map[string]any         `json:"metadata"`
	TopP      float64                `json:"top_p"`
	Extra     map[string]json.Number `json:"extra"`
}

// The canonical request types decode through their own UnmarshalJSON, which
// used to cost the error its position and with it the member's name.
func TestNewInvalidRequestBodyErrorNamesMemberOnCanonicalRequests(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		decode func([]byte) error
		want   string
	}{
		{
			name:   "chat completions",
			body:   `{"model":"gpt-4.1-mini","messages":"hi"}`,
			decode: func(body []byte) error { return json.Unmarshal(body, &ChatRequest{}) },
			want:   "invalid request body: messages: must be an array",
		},
		{
			name:   "responses",
			body:   `{"model":"gpt-4.1-mini","input":"hi","temperature":"warm"}`,
			decode: func(body []byte) error { return json.Unmarshal(body, &ResponsesRequest{}) },
			want:   "invalid request body: temperature: must be a number",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decode([]byte(tc.body))
			if err == nil {
				t.Fatalf("body %q decoded without error", tc.body)
			}
			if got := NewInvalidRequestBodyError([]byte(tc.body), err).Message; got != tc.want {
				t.Errorf("message = %q, want %q", got, tc.want)
			}
		})
	}
}

// Decoder wording must never reach the client: goccy reports a type mismatch
// inside a complete document as "unexpected end of JSON input" and names Go
// types and struct fields. Each case decodes for real, so the expectations
// track the decoder the gateway actually uses.
func TestNewInvalidRequestBodyError(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		message   string
		param     string
		unwantSub string
	}{
		{
			name:    "array where object expected",
			body:    `{"model":"m","messages":"hi"}`,
			message: "invalid request body: messages: must be an array",
			param:   "messages",
		},
		{
			name:    "string where integer expected",
			body:    `{"model":"m","max_tokens":"10"}`,
			message: "invalid request body: max_tokens: must be an integer",
			param:   "max_tokens",
		},
		{
			name:    "string where boolean expected",
			body:    `{"model":"m","stream":"yes"}`,
			message: "invalid request body: stream: must be a boolean",
			param:   "stream",
		},
		{
			name:    "string where object expected",
			body:    `{"model":"m","metadata":"abc"}`,
			message: "invalid request body: metadata: must be an object",
			param:   "metadata",
		},
		{
			name:    "string where number expected",
			body:    `{"model":"m","top_p":"x"}`,
			message: "invalid request body: top_p: must be a number",
			param:   "top_p",
		},
		{
			name:    "number where string expected",
			body:    `{"model":5}`,
			message: "invalid request body: model: must be a string",
			param:   "model",
		},
		{
			name:    "nested member is named with its index",
			body:    `{"messages":[{"role":"user"},{"role":5}]}`,
			message: "invalid request body: messages[1].role: must be a string",
			param:   "messages[1].role",
		},
		{
			name:    "truncated body",
			body:    `{"model":"m","max_tokens":1`,
			message: "invalid request body: request body is not valid JSON: the document ends unexpectedly",
		},
		{
			name:    "empty body",
			body:    "",
			message: "invalid request body: request body is empty",
		},
		{
			name:    "top-level array",
			body:    `[1,2,3]`,
			message: "invalid request body: request body must be a JSON object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req decodeTestRequest
			err := json.Unmarshal([]byte(tc.body), &req)
			if err == nil {
				t.Fatalf("body %q decoded without error", tc.body)
			}
			gatewayErr := NewInvalidRequestBodyError([]byte(tc.body), err)
			if gatewayErr.Message != tc.message {
				t.Errorf("message = %q, want %q", gatewayErr.Message, tc.message)
			}
			param := ""
			if gatewayErr.Param != nil {
				param = *gatewayErr.Param
			}
			if param != tc.param {
				t.Errorf("param = %q, want %q", param, tc.param)
			}
			for _, leak := range []string{"json:", "Go struct field", "unexpected end of JSON input", "goccy"} {
				if strings.Contains(gatewayErr.Message, leak) {
					t.Errorf("message %q leaks decoder internals %q", gatewayErr.Message, leak)
				}
			}
		})
	}
}
