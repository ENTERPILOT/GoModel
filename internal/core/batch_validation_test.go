package core

import (
	"context"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchInputFile serves one JSONL input file and records the rewritten one.
type batchInputFile struct {
	data    []byte
	created []byte
}

func (f *batchInputFile) GetFileContent(context.Context, string, string) (*FileContentResponse, error) {
	return &FileContentResponse{Data: f.data}, nil
}

func (f *batchInputFile) CreateFile(_ context.Context, _ string, req *FileCreateRequest) (*FileObject, error) {
	f.created = req.Content
	return &FileObject{ID: "file-rewritten"}, nil
}

// checkedBatchItems runs the input file through RewriteBatchSource with an
// identity rewrite, recording the custom_id of each item it checked.
func checkedBatchItems(t *testing.T, endpoint string, file *batchInputFile, operations ...Operation) ([]string, error) {
	t.Helper()
	var checked []string
	_, err := RewriteBatchSource(context.Background(), "openai", &BatchRequest{InputFileID: "file-in", Endpoint: endpoint}, file, operations,
		func(_ context.Context, item BatchRequestItem, _ *DecodedBatchItemRequest) (json.RawMessage, error) {
			checked = append(checked, item.CustomID)
			return item.Body, nil
		})
	return checked, err
}

const batchChatBody = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`

// A line the gateway cannot check is refused rather than forwarded: its model
// access and guardrails would otherwise never run.
func TestRewriteBatchSource_RefusesLinesItCannotCheck(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{name: "legacy completions", line: `{"custom_id":"1","method":"POST","url":"/v1/completions","body":{"model":"gpt-3.5-turbo-instruct","prompt":"hi"}}`, wantErr: `unsupported batch url "/v1/completions"`},
		{name: "moderations", line: `{"custom_id":"1","method":"POST","url":"/v1/moderations","body":{"model":"omni-moderation-latest","input":"hi"}}`, wantErr: "unsupported batch url"},
		{name: "doubled slash", line: `{"custom_id":"1","url":"/v1//chat/completions","body":` + batchChatBody + `}`, wantErr: "unsupported batch url"},
		{name: "upper-case path", line: `{"custom_id":"1","url":"/V1/chat/completions","body":` + batchChatBody + `}`, wantErr: "unsupported batch url"},
		{name: "path below a supported endpoint", line: `{"custom_id":"1","url":"/v1/responses/compact","body":{"model":"gpt-4o-mini","input":"hi"}}`, wantErr: "unsupported batch url"},
		{name: "non-POST method", line: `{"custom_id":"1","method":"GET","url":"/v1/chat/completions","body":` + batchChatBody + `}`, wantErr: "batch items must use POST"},
		{name: "no body", line: `{"custom_id":"1","url":"/v1/chat/completions"}`, wantErr: "body is required"},
		{name: "body twice", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","messages":[]},"body":` + batchChatBody + `}`, wantErr: `key "body" is repeated`},
		{name: "body and Body", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","messages":[]},"Body":` + batchChatBody + `}`, wantErr: `key "Body" is not a batch item field`},
		{name: "model twice", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","model":"gpt-4o-mini","messages":[]}}`, wantErr: `key "model" is repeated`},
		{name: "model repeated through an escape", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","mo\u0064el":"gpt-4o-mini","messages":[]}}`, wantErr: `key "model" is repeated`},
		{name: "repeat after a repeated empty key", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"":0,"":1,"model":"forbidden","model":"gpt-4o-mini","messages":[]}}`, wantErr: `key "" is repeated`},
		{name: "model and Model", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","Model":"gpt-4o-mini","messages":[]}}`, wantErr: `keys "model" and "Model" differ only in case`},
		{name: "nested key repeated", line: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x","content":"y"}]}}`, wantErr: `key "content" is repeated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checked, err := checkedBatchItems(t, "/v1/chat/completions", &batchInputFile{data: []byte(tt.line + "\n")},
				OperationChatCompletions, OperationResponses, OperationEmbeddings)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "batch input file line 1")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, checked)
		})
	}
}

// An item a preparer does not rewrite (guardrails leave embeddings alone) is
// still refused when the gateway cannot check it.
func TestRewriteBatchSource_RefusesUncheckableLinesOutsideTheOperations(t *testing.T) {
	file := &batchInputFile{data: []byte(`{"custom_id":"1","url":"/v1/moderations","body":{"input":"hi"}}` + "\n")}

	_, err := checkedBatchItems(t, "/v1/chat/completions", file, OperationChatCompletions, OperationResponses)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported batch url")
}

func TestRewriteBatchSource_RefusesAnUnsupportedBatchEndpoint(t *testing.T) {
	_, err := checkedBatchItems(t, "/v1/completions", &batchInputFile{}, OperationChatCompletions)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported batch endpoint "/v1/completions"`)
}

// Supported lines are checked and forwarded unchanged, including the
// unversioned paths Azure OpenAI batch files use and keys that differ only in
// case below the top level, as a tool schema's properties can.
func TestRewriteBatchSource_ForwardsCheckableLinesUnchanged(t *testing.T) {
	lines := strings.Join([]string{
		`{"custom_id":"chat","method":"POST","url":"/v1/chat/completions","body":` + batchChatBody + `}`,
		`{"custom_id":"azure","url":"/chat/completions","body":` + batchChatBody + `}`,
		`{"custom_id":"default","body":` + batchChatBody + `}`,
		`{"custom_id":"schema","url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"id":{"type":"string"},"ID":{"type":"string"}}}}}]}}`,
		`{"custom_id":"embed","url":"/v1/embeddings","body":{"model":"text-embedding-3-small","input":"hi"}}`,
	}, "\n") + "\n"
	file := &batchInputFile{data: []byte(lines)}

	checked, err := checkedBatchItems(t, "/v1/chat/completions", file, OperationChatCompletions, OperationResponses)

	require.NoError(t, err)
	assert.Equal(t, []string{"chat", "azure", "default", "schema"}, checked)
	assert.Nil(t, file.created, "unchanged lines need no new input file")
}

// A URL accepted generously is forwarded as the path that was checked, so
// the provider runs the endpoint the gateway authorized.
func TestRewriteBatchSource_ForwardsTheCheckedPath(t *testing.T) {
	file := &batchInputFile{data: []byte(`{"custom_id":"1","url":"https://provider.example/v1/responses/?trace=1","body":{"model":"gpt-4o-mini","input":"hi"}}` + "\n")}

	checked, err := checkedBatchItems(t, "/v1/chat/completions", file, OperationResponses)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, checked)
	require.NotNil(t, file.created)
	var forwarded BatchRequestItem
	require.NoError(t, json.Unmarshal(file.created, &forwarded))
	assert.Equal(t, "/v1/responses", forwarded.URL)
}

// Inline items follow the same rules, including a case variant of an item
// field, which would otherwise be forwarded beside the checked body.
func TestRewriteBatchSource_ChecksInlineItems(t *testing.T) {
	tests := []struct {
		name    string
		item    string
		wantErr string
		wantURL string
	}{
		{name: "unsupported url", item: `{"custom_id":"1","url":"/v1/completions","body":{"model":"m","prompt":"hi"}}`, wantErr: "batch item 0: unsupported batch url"},
		{name: "body twice", item: `{"custom_id":"1","url":"/v1/chat/completions","body":{"model":"forbidden","messages":[]},"body":` + batchChatBody + `}`, wantErr: `key "body" is repeated`},
		{name: "url twice", item: `{"custom_id":"1","url":"/v1/completions","url":"/v1/chat/completions","body":` + batchChatBody + `}`, wantErr: `key "url" is repeated`},
		{name: "case variant of body", item: `{"custom_id":"1","url":"/v1/chat/completions","body":` + batchChatBody + `,"Body":{"model":"forbidden","messages":[]}}`, wantErr: `key "Body" is not a batch item field`},
		{name: "model and Model", item: `{"custom_id":"1","url":"/v1/chat/completions","body":{"Model":"forbidden","model":"gpt-4o-mini","messages":[]}}`, wantErr: "differ only in case"},
		{name: "full url", item: `{"custom_id":"1","url":"https://provider.example/v1/chat/completions/","body":` + batchChatBody + `}`, wantURL: "/v1/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req BatchRequest
			if err := json.Unmarshal([]byte(`{"endpoint":"/v1/chat/completions","requests":[`+tt.item+`]}`), &req); err != nil {
				require.NotEmpty(t, tt.wantErr, "decode: %v", err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			result, err := RewriteBatchSource(context.Background(), "openai", &req, nil, []Operation{OperationChatCompletions},
				func(_ context.Context, item BatchRequestItem, _ *DecodedBatchItemRequest) (json.RawMessage, error) {
					return item.Body, nil
				})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, result.Request.Requests[0].URL)
		})
	}
}
