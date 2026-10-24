package core_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// fuzzBatchFiles serves one input file and records the upload.
type fuzzBatchFiles struct {
	content  []byte
	uploaded []byte
}

func (f *fuzzBatchFiles) GetFileContent(_ context.Context, _, id string) (*core.FileContentResponse, error) {
	return &core.FileContentResponse{ID: id, Filename: "input.jsonl", Data: f.content}, nil
}

func (f *fuzzBatchFiles) CreateFile(_ context.Context, _ string, req *core.FileCreateRequest) (*core.FileObject, error) {
	f.uploaded = req.Content
	return &core.FileObject{ID: "file-rewritten", Object: "file", Filename: req.Filename, Purpose: req.Purpose}, nil
}

// fuzzRewrittenBody replaces every rewritten line's body in rewrite mode.
const fuzzRewrittenBody = `{"rewritten":true}`

// FuzzRewriteBatchSource feeds arbitrary JSONL input files through batch
// preparation, with an identity rewrite or one that replaces the body.
// Whatever the file holds, preparation must not panic and must fail only with
// a gateway error. When it uploads a rewritten file, the batch must point at
// that file, and every line of it must be JSON carrying the rewrite.
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
		f.Add(seed.endpoint, []byte(seed.content), false)
		f.Add(seed.endpoint, []byte(seed.content), true)
	}

	f.Fuzz(func(t *testing.T, endpoint string, content []byte, replace bool) {
		req := &core.BatchRequest{InputFileID: "file-input", Endpoint: endpoint}
		rewrite := func(_ context.Context, item core.BatchRequestItem, _ *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			if replace {
				return json.RawMessage(fuzzRewrittenBody), nil
			}
			return item.Body, nil
		}
		files := &fuzzBatchFiles{content: content}

		result, err := core.RewriteBatchSource(t.Context(), "openai", req, files, nil, rewrite)
		if err != nil {
			var gatewayErr *core.GatewayError
			assert.ErrorAs(t, err, &gatewayErr, "error %T is not a gateway error", err)
			return
		}
		require.NotNil(t, result)
		require.NotNil(t, result.Request)
		if files.uploaded == nil {
			return
		}
		assert.Equal(t, "file-rewritten", result.RewrittenInputFileID)
		assert.Equal(t, "file-rewritten", result.Request.InputFileID)
		if replace {
			assert.Contains(t, string(files.uploaded), `"body":`+fuzzRewrittenBody)
		}
		for line := range bytes.SplitSeq(files.uploaded, []byte("\n")) {
			if line = bytes.TrimSpace(line); len(line) > 0 {
				assert.True(t, json.Valid(line), "uploaded line is not JSON: %q", line)
			}
		}
	})
}
