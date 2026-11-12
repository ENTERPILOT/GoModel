package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// editBody runs body through a jsonUsageEditor in pieces of size bytes.
func editBody(edit usageCostEdit, body string, size int) string {
	editor := &jsonUsageEditor{edit: edit}
	var out strings.Builder
	for rest := body; len(rest) > 0; {
		n := min(size, len(rest))
		out.Write(editor.feed([]byte(rest[:n])))
		rest = rest[n:]
	}
	out.Write(editor.flush())
	return out.String()
}

func TestJSONUsageEditor(t *testing.T) {
	const cost = `"cost":0.00002`
	tests := []struct {
		name    string
		body    string
		unknown bool
		billed  bool
		want    string // empty: the body is relayed as it is
	}{
		{
			name: "chat completion",
			body: `{"id":"chatcmpl-1","choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":10,"total_tokens":15}}`,
			want: `{"id":"chatcmpl-1","choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":10,"total_tokens":15,` + cost + `}}`,
		},
		{
			name: "usage first, then more members",
			body: `{"usage":{"input_tokens":10},"id":"msg_1"}`,
			want: `{"usage":{"input_tokens":10,` + cost + `},"id":"msg_1"}`,
		},
		{
			name: "openrouter: the upstream's cost is replaced, its details kept",
			body: `{"usage":{"total_tokens":15,"cost":0.9,"is_byok":false,"cost_details":{"upstream_inference_cost":0.8}}}`,
			want: `{"usage":{"total_tokens":15,"is_byok":false,"cost_details":{"upstream_inference_cost":0.8},` + cost + `}}`,
		},
		{
			name:   "openrouter billed: cost and details are the gateway's",
			body:   `{"usage":{"total_tokens":15,"cost":0.9,"cost_details":{"upstream_inference_cost":0.8}}}`,
			billed: true,
			want:   `{"usage":{"total_tokens":15,` + cost + `}}`,
		},
		{
			name:    "openrouter without pricing: no cost reaches the client",
			body:    `{"usage":{"total_tokens":15,"cost":0.9,"cost_details":{"upstream_inference_cost":0.8}}}`,
			unknown: true,
			want:    `{"usage":{"total_tokens":15,"cost_details":{"upstream_inference_cost":0.8}}}`,
		},
		{
			name: "pretty printed",
			body: "{\n  \"id\": \"x\",\n  \"usage\": {\n    \"cost\": 1,\n    \"total_tokens\": 15\n  }\n}\n",
			want: "{\n  \"id\": \"x\",\n  \"usage\": {\n    \"total_tokens\": 15," + cost + "\n  }\n}\n",
		},
		{
			name: "duplicate, cased and escaped members and usage objects",
			body: `{"usage":{"cost":1,"total_tokens":15,"COST":2},"Usage":{"coſt":3},"usage":{"cost":4}}`,
			want: `{"usage":{"total_tokens":15,` + cost + `},"Usage":{},"usage":{` + cost + `}}`,
		},
		{
			name: "strings that look like usage are not members",
			body: `{"text":"\"usage\":{\"cost\":1}","choices":[{"usage":{"cost":2}}],"usage":{"total_tokens":15}}`,
			want: `{"text":"\"usage\":{\"cost\":1}","choices":[{"usage":{"cost":2}}],"usage":{"total_tokens":15,` + cost + `}}`,
		},
		{
			name: "large integers and escapes elsewhere keep every byte",
			body: `{"created":9007199254740993,"seed":123456789012345678901234567890,"text":"café \/ 😀","usage":{"total_tokens":12345678901234567890,"cost":1}}`,
			want: `{"created":9007199254740993,"seed":123456789012345678901234567890,"text":"café \/ 😀","usage":{"total_tokens":12345678901234567890,` + cost + `}}`,
		},
		{
			name: "empty usage",
			body: `{"usage":{}}`,
			want: `{"usage":{` + cost + `}}`,
		},
		{name: "usage null", body: `{"usage":null,"id":"x"}`},
		{name: "no usage", body: `{"data":[{"b64_json":"aGk="}]}`},
		{name: "unknown cost and nothing to remove", body: `{"usage":{"total_tokens":15}}`, unknown: true},
		{name: "top-level array", body: `[{"usage":{"cost":1}}]`},
		{name: "not JSON", body: `data: {"usage":{"cost":1}}`},
		{name: "truncated", body: `{"usage":{"cost":1,"total_tokens":`},
		{name: "after the object", body: `{"id":"x"} {"usage":{"cost":1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edit := newUsageCostEdit(0.00002, !tt.unknown, tt.billed)
			want := tt.want
			if want == "" {
				want = tt.body
			}
			// Every split of the body gives the same answer.
			for _, size := range []int{1, 2, 7, 64, len(tt.body)} {
				assert.Equal(t, want, editBody(edit, tt.body, size), "write size %d", size)
			}
			if tt.want == "" || !gjson.Valid(tt.want) {
				return
			}
			wantCost := "0.00002"
			if tt.unknown {
				wantCost = ""
			}
			assertOneGatewayCost(t, []byte(tt.want), wantCost)
			if tt.billed {
				assertNoCostDetails(t, []byte(tt.want))
			}
		})
	}
}

// A buffered JSON response gets the cost in its usage and in its headers,
// the same rounded value, and loses a stale Content-Length.
func TestResponseCostWriter_EditsJSONBodies(t *testing.T) {
	const body = `{"id":"x","usage":{"total_tokens":3,"cost":9}}`
	tests := []struct {
		name      string
		editsJSON bool
		want      string
	}{
		{name: "model answer", editsJSON: true, want: `{"id":"x","usage":{"total_tokens":3,"cost":0.5}}`},
		{name: "batch, file or conversation", want: body},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := costWriterFor(t, http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Content-Length": {"47"}})
			w.editsJSON = tt.editsJSON

			_, err := w.Write([]byte(body))
			require.NoError(t, err)
			w.finish()

			assert.Equal(t, tt.want, rec.Body.String())
			assert.Equal(t, "0.5", rec.Header().Get(responseCostHeader))
			if tt.editsJSON {
				assert.Empty(t, rec.Header().Get("Content-Length"))
				assert.Equal(t, rec.Header().Get(responseCostHeader), gjson.Get(rec.Body.String(), "usage.cost").Raw)
			}
		})
	}
}

func TestEditsJSONBodies(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/embeddings", "/v1/images/generations", "/v1/decisions", "/p/openrouter/chat/completions"} {
		assert.True(t, editsJSONBodies(core.DescribeEndpointPath(path).Operation), path)
	}
	for _, path := range []string{"/v1/batches", "/v1/batches/batch_1", "/v1/messages/batches/b_1", "/v1/files/file_1", "/v1/conversations/conv_1"} {
		assert.False(t, editsJSONBodies(core.DescribeEndpointPath(path).Operation), path)
	}
}
