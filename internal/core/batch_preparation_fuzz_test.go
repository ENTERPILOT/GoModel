package core_test

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// fuzzBatchFiles serves one input file and accepts any upload.
type fuzzBatchFiles struct {
	content []byte
}

func (f *fuzzBatchFiles) GetFileContent(_ context.Context, _, id string) (*core.FileContentResponse, error) {
	return &core.FileContentResponse{ID: id, Filename: "input.jsonl", Data: f.content}, nil
}

func (f *fuzzBatchFiles) CreateFile(_ context.Context, _ string, req *core.FileCreateRequest) (*core.FileObject, error) {
	return &core.FileObject{ID: "file-rewritten", Object: "file", Filename: req.Filename, Purpose: req.Purpose}, nil
}

// FuzzRewriteBatchSource feeds arbitrary JSONL input files through batch
// preparation with an identity rewrite. Whatever the file holds, preparation
// must not panic and must fail only with a gateway error.
func FuzzRewriteBatchSource(f *testing.F) {
	for _, seed := range []struct {
		endpoint string
		content  string
	}{
		{"/v1/chat/completions", `{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}` + "\n"},
		{"/v1/embeddings", `{"custom_id":"b","method":"POST","url":"/v1/embeddings","body":{"model":"text-embedding-3-small","input":"hi"}}`},
		{"/v1/responses", `{"custom_id":"c","url":"/v1/responses","body":{"model":"gpt-4o-mini","input":"hi"}}` + "\r\n\r\n"},
		{"", `{"custom_id":"d","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[]}}` + "\n" + `{"custom_id":"e","method":"GET","url":"/v1/models","body":null}` + "\n"},
		{"/v1/chat/completions", `{"custom_id":"f","url":"","body":{}}`},
		{"/v1/chat/completions", `{"custom_id":"g","url":"/v1/chat/completions","body":"not an object"}`},
		{"/v1/chat/completions", `{"custom_id":"h","url":"/v1/chat/completions","body":{"model":1}}`},
		{"/v1/chat/completions", `{"custom_id":"i"`},
		{"/v1/chat/completions", `[]`},
		{"/v1/chat/completions", "\n\n   \n"},
		{"/v1/chat/completions", ""},
	} {
		f.Add(seed.endpoint, []byte(seed.content))
	}

	f.Fuzz(func(t *testing.T, endpoint string, content []byte) {
		req := &core.BatchRequest{InputFileID: "file-input", Endpoint: endpoint}
		identity := func(_ context.Context, item core.BatchRequestItem, _ *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			return item.Body, nil
		}

		result, err := core.RewriteBatchSource(t.Context(), "openai", req, &fuzzBatchFiles{content: content}, nil, identity)
		if err != nil {
			var gatewayErr *core.GatewayError
			assert.ErrorAs(t, err, &gatewayErr, "error %T is not a gateway error", err)
			return
		}
		require.NotNil(t, result)
		assert.NotNil(t, result.Request)
	})
}
