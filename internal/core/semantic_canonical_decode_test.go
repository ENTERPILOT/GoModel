package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The canonical decode calls a request type's UnmarshalJSON directly to skip
// json.Unmarshal's body copies. That shortcut must be invisible: the decoded
// value, the error text and the client-facing description have to match what
// json.Unmarshal produces for the same body.
func TestCanonicalDecodeMatchesJSONUnmarshal(t *testing.T) {
	decoders := []struct {
		name      string
		canonical func([]byte) (any, error)
		reference func([]byte) (any, error)
	}{
		{
			name:      "chat",
			canonical: func(body []byte) (any, error) { return DecodeChatRequest(body, nil) },
			reference: func(body []byte) (any, error) { req := &ChatRequest{}; return req, json.Unmarshal(body, req) },
		},
		{
			name:      "responses",
			canonical: func(body []byte) (any, error) { return DecodeResponsesRequest(body, nil) },
			reference: func(body []byte) (any, error) { req := &ResponsesRequest{}; return req, json.Unmarshal(body, req) },
		},
		{
			name:      "embeddings",
			canonical: func(body []byte) (any, error) { return DecodeEmbeddingRequest(body, nil) },
			reference: func(body []byte) (any, error) { req := &EmbeddingRequest{}; return req, json.Unmarshal(body, req) },
		},
		{
			name:      "batches",
			canonical: func(body []byte) (any, error) { return DecodeBatchRequest(body, nil) },
			reference: func(body []byte) (any, error) { req := &BatchRequest{}; return req, json.Unmarshal(body, req) },
		},
	}
	bodies := []struct {
		name string
		body string
	}{
		{name: "valid", body: `{"model":"gpt-4o-mini","input":"hi","messages":[{"role":"user","content":"hi"}],"x":{"a":1}}`},
		{name: "valid behind whitespace", body: "\r\n\t {\"model\":\"gpt-4o-mini\",\"input\":\"hi\"} \n"},
		{name: "wrong type", body: `{"model":5,"temperature":"hot","dimensions":"x","requests":5}`},
		{name: "wrong type behind whitespace", body: "\n\n      {\"model\":5,\"temperature\":\"hot\",\"dimensions\":\"x\",\"requests\":5}"},
		{name: "wrong type in a later member behind whitespace", body: "  \t {\"model\":\"a\",\"stream\":\"yes\",\"dimensions\":\"x\",\"requests\":[{\"custom_id\":5}]}"},
		{name: "malformed", body: `{"model":"a",}`},
		{name: "malformed behind whitespace", body: `   {"model":"a",}`},
		{name: "truncated", body: `{"model":"a","messages":[`},
		{name: "trailing data", body: `{"model":"a"} trailing`},
		{name: "second document", body: `{"model":"a"}{"model":"b"}`},
		{name: "empty", body: ``},
		{name: "whitespace only", body: " \n"},
		{name: "null", body: `null`},
		{name: "array", body: `[{"model":"a"}]`},
		{name: "string", body: `"model"`},
	}

	for _, decoder := range decoders {
		for _, tt := range bodies {
			t.Run(decoder.name+"/"+tt.name, func(t *testing.T) {
				body := []byte(tt.body)
				want, wantErr := decoder.reference(body)
				got, err := decoder.canonical(body)

				if wantErr == nil {
					require.NoError(t, err)
					assert.Equal(t, want, got)
					return
				}
				require.Error(t, err)
				assert.Equal(t, wantErr.Error(), err.Error())
				wantDetail, wantParam := describeJSONDecodeError(body, wantErr)
				detail, param := describeJSONDecodeError(body, err)
				assert.Equal(t, wantDetail, detail)
				assert.Equal(t, wantParam, param)
			})
		}
	}
}

// Leading whitespace is legal JSON, and it must not cost a decode error the
// name of the member that caused it.
func TestCanonicalDecodeNamesMemberBehindLeadingWhitespace(t *testing.T) {
	body := []byte("\n  {\"model\":\"gpt-4o-mini\",\"messages\":\"hi\"}")

	_, err := DecodeChatRequest(body, nil)

	require.Error(t, err)
	gatewayErr := NewInvalidRequestBodyError(body, err)
	assert.Equal(t, "invalid request body: messages: must be an array", gatewayErr.Message)
	require.NotNil(t, gatewayErr.Param)
	assert.Equal(t, "messages", *gatewayErr.Param)
}
