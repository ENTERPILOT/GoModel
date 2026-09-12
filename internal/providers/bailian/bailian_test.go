package bailian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ core.NativeBatchProvider = (*Provider)(nil)
	_ core.NativeFileProvider  = (*Provider)(nil)
)

const chatCompletionJSON = `{
	"id":"chatcmpl-bailian",
	"created":1677652288,
	"model":"qwen3-max",
	"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}
}`

const upstreamErrorJSON = `{"error":{"message":"bad request","type":"invalid_request_error"}}`

// newTestProvider points a provider at the given upstream URL.
func newTestProvider(baseURL string) *Provider {
	p := NewWithHTTPClient("key", nil, llmclient.Hooks{})
	p.SetBaseURL(baseURL)
	return p
}

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "bailian",
		DefaultBaseURL: defaultBaseURL,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			p := NewWithHTTPClient(apiKey, client, hooks)
			p.SetBaseURL(baseURL)
			return p
		},
		Embeddings: true,
	})
}

func TestChatCompletion_MaxTokensMapping(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	maxTokens := 4096
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "qwen3-max",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "max_tokens")
	assert.Equal(t, float64(4096), sent["max_completion_tokens"])
}

func TestChatCompletion_NoMaxTokensMapping(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.NotContains(t, capture.Last(t).JSON(t), "max_completion_tokens")
}

func TestStreamChatCompletion_MaxTokensMapping(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")
	provider := newTestProvider(server.URL)

	maxTokens := 2048
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "qwen3-flash",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	})
	require.NoError(t, err)
	defer stream.Close()

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "max_tokens")
	assert.Equal(t, float64(2048), sent["max_completion_tokens"])
}

func TestPassthrough_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"ok":true}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPassthrough_NilRequest(t *testing.T) {
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})
	_, err := provider.Passthrough(context.Background(), nil)
	require.Error(t, err)
}

func TestPassthrough_ReadError(t *testing.T) {
	readErr := errors.New("read failed")
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})

	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     errReadCloser{err: readErr},
	})
	require.ErrorIs(t, err, readErr)
}

func TestAdaptBailianRequest_Nil(t *testing.T) {
	r, err := adaptChatRequest(nil)
	require.NoError(t, err)
	assert.Nil(t, r)
}

func TestAdaptBailianRequest_NoMaxTokens(t *testing.T) {
	req := &core.ChatRequest{Model: "qwen3-max"}
	r, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.Nil(t, r.MaxTokens)
}

func TestStreamResponses_DelegatesToChat(t *testing.T) {
	server, capture := providertest.SSEServer(t,
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	provider := newTestProvider(server.URL)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "qwen3-max",
		Input: "hello",
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.NotEmpty(t, body)
	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
}

func TestAdaptBailianRequest_PreservesOtherFields(t *testing.T) {
	maxTokens := 100
	req := &core.ChatRequest{
		Model:     "qwen3-max",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	}
	r, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.NotEqual(t, req, r)
	assert.Equal(t, "qwen3-max", r.Model)
	assert.Nil(t, r.MaxTokens)
}

func TestAdaptBailianRequest_RespectsExistingMaxCompletionTokens(t *testing.T) {
	extra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"max_completion_tokens": json.RawMessage(`200`),
	})
	maxTokens := 100
	req := &core.ChatRequest{
		Model:       "qwen3-max",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   &maxTokens,
		ExtraFields: extra,
	}
	r, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.NotEqual(t, req, r)
	assert.Nil(t, r.MaxTokens)

	body, err := json.Marshal(r)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.NotContains(t, raw, "max_tokens")
	require.Contains(t, raw, "max_completion_tokens")

	var mct int
	require.NoError(t, json.Unmarshal(raw["max_completion_tokens"], &mct))
	assert.Equal(t, 200, mct)
}

func TestChatCompletion_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, upstreamErrorJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
}

func TestChatCompletion_TransportFailure(t *testing.T) {
	errTransport := errors.New("simulated transport failure")
	provider := NewWithHTTPClient("key", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errTransport
		}),
	}, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
}

func TestStreamChatCompletion_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"error":{"message":"unauthorized","type":"authentication_error"}}`)
	provider := newTestProvider(server.URL)

	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
}

func TestResponses_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, upstreamErrorJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "qwen3-max",
		Input: "hello",
	})
	require.Error(t, err)
}

func TestEmbeddings_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, upstreamErrorJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-v3",
		Input: "test",
	})
	require.Error(t, err)
}

func TestPassthrough_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":"internal"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestListModels_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":"internal"}`)
	provider := newTestProvider(server.URL)

	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
}

func TestCreateBatch_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"batch-bailian-1","object":"batch","status":"validating"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.CreateBatch(context.Background(), &core.BatchRequest{
		InputFileID: "file-1",
		Endpoint:    "/v1/chat/completions",
	})
	require.NoError(t, err)
	assert.Equal(t, "batch-bailian-1", resp.ID)
}

func TestGetBatch_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"batch-bailian-1","object":"batch","status":"completed"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.GetBatch(context.Background(), "batch-bailian-1")
	require.NoError(t, err)
	assert.Equal(t, "batch-bailian-1", resp.ID)
}

func TestListBatches_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	provider := newTestProvider(server.URL)

	_, err := provider.ListBatches(context.Background(), 10, "")
	require.NoError(t, err)
}

func TestCancelBatch_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"batch-bailian-1","object":"batch","status":"cancelling"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.CancelBatch(context.Background(), "batch-bailian-1")
	require.NoError(t, err)
	assert.Equal(t, "batch-bailian-1", resp.ID)
}

func TestCreateFile_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"file-1","object":"file","purpose":"batch","bytes":100}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.CreateFile(context.Background(), &core.FileCreateRequest{
		Content: []byte("data"),
		Purpose: "batch",
	})
	require.NoError(t, err)
	assert.Equal(t, "bailian", resp.Provider)
}

func TestDeleteFile_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"file-1","object":"file","deleted":true}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.DeleteFile(context.Background(), "file-1")
	require.NoError(t, err)
	assert.True(t, resp.Deleted)
}

func TestListFiles_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	provider := newTestProvider(server.URL)

	_, err := provider.ListFiles(context.Background(), "batch", 10, "")
	require.NoError(t, err)
}

func TestGetFile_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"file-1","object":"file","purpose":"batch"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.GetFile(context.Background(), "file-1")
	require.NoError(t, err)
	assert.Equal(t, "bailian", resp.Provider)
}

func TestGetFileContent_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"text":"content"}`)
	provider := newTestProvider(server.URL)

	_, err := provider.GetFileContent(context.Background(), "file-1")
	require.NoError(t, err)
}

func TestGetBatchResults_Delegates(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"batch-1","output_file_id":"file-out-1"}`)
	provider := newTestProvider(server.URL)

	_, err := provider.GetBatchResults(context.Background(), "batch-1")
	require.NoError(t, err)
}

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct {
	err error
}

func (r errReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errReadCloser) Close() error {
	return nil
}

func TestPassthrough_MaxTokensMapping(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"chatcmpl-bailian","model":"qwen3-max"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}],"max_tokens":4096}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "max_tokens")
	assert.Equal(t, float64(4096), sent["max_completion_tokens"])
}

func TestPassthrough_PreservesExistingMaxCompletionTokens(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"chatcmpl-bailian","model":"qwen3-max"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"max_completion_tokens":200}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	// The explicit max_completion_tokens wins over the mapped max_tokens.
	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "max_tokens")
	assert.Equal(t, float64(200), sent["max_completion_tokens"])
}

func TestPassthrough_NoMaxTokens(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"chatcmpl-bailian","model":"qwen3-max"}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}]}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotContains(t, capture.Last(t).JSON(t), "max_completion_tokens")
}
