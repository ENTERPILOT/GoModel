package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// findRequest returns the first recorded request matching method and path.
func findRequest(capture *providertest.Capture, method, path string) (providertest.Recorded, bool) {
	for _, req := range capture.All() {
		if req.Method == method && req.Path == path {
			return req, true
		}
	}
	return providertest.Recorded{}, false
}

func TestCompatibleProvider_ListModels_ReturnsUpstreamOnSuccess(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, providertest.ModelsJSON)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{ProviderName: "upstream-only", BaseURL: server.URL},
	)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, providertest.Model, resp.Data[0].ID)
}

func TestCompatibleProvider_ListModels_DefaultsMissingObjectFields(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":[{"id":"openrouter/model","object":"","owned_by":"openrouter"}]}`)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{ProviderName: "openrouter", BaseURL: server.URL},
	)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "model", resp.Data[0].Object)
}

func TestCompatibleProvider_ListModels_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.Server(t, http.NotFound)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{ProviderName: "test-provider", BaseURL: server.URL},
	)

	_, err := provider.ListModels(context.Background())
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Contains(t, []core.ErrorType{core.ErrorTypeProvider, core.ErrorTypeNotFound}, gatewayErr.Type)
}

func TestCompatibleProvider_AdaptChatRequest_RewritesBodyOnChatAndStream(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"resp","model":"quirk-1","choices":[]}`)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "quirky",
			BaseURL:      server.URL,
			AdaptChatRequest: func(req *core.ChatRequest) (*core.ChatRequest, error) {
				adapted := *req
				adapted.User = "adapted-user"
				return &adapted, nil
			},
		},
	)

	original := &core.ChatRequest{Model: "quirk-1", User: "original-user"}
	_, err := provider.ChatCompletion(context.Background(), original)
	require.NoError(t, err)

	stream, err := provider.StreamChatCompletion(context.Background(), original)
	require.NoError(t, err)
	_, _ = io.ReadAll(stream)
	stream.Close()

	requests := capture.All()
	require.Len(t, requests, 2)
	for _, req := range requests {
		assert.Equal(t, "adapted-user", req.JSON(t)["user"])
	}
	assert.Equal(t, "original-user", original.User, "the caller's request must not be mutated")
}

func TestCompatibleProvider_AdaptChatRequest_ErrorAborts(t *testing.T) {
	server, capture := providertest.Server(t, nil)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "quirky",
			BaseURL:      server.URL,
			AdaptChatRequest: func(*core.ChatRequest) (*core.ChatRequest, error) {
				return nil, core.NewInvalidRequestError("cannot adapt", nil)
			},
		},
	)
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	require.Error(t, err)
	_, err = provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	require.Error(t, err)
	assert.Zero(t, capture.Count(), "upstream must not be called when adaptation fails")
}

func TestCompatibleProvider_ChatRequestHeaders_AppliedToChatOnly(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models":           jsonHandler(`{"object":"list","data":[]}`),
		"/chat/completions": jsonHandler(`{"id":"resp","model":"m","choices":[]}`),
	})
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "affine",
			BaseURL:      server.URL,
			ChatRequestHeaders: func(_ context.Context, req *core.ChatRequest) http.Header {
				h := make(http.Header, 1)
				h.Set("X-Conv-Id", "conv-"+req.Model)
				return h
			},
		},
	)
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	require.NoError(t, err)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	require.NoError(t, err)
	_, _ = io.ReadAll(stream)
	stream.Close()

	_, err = provider.ListModels(context.Background())
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 3)
	chatRequests := 0
	for _, req := range requests {
		want := ""
		if req.Path == "/chat/completions" {
			want = "conv-m"
			chatRequests++
		}
		assert.Equal(t, want, req.Header.Get("X-Conv-Id"), req.Path)
	}
	assert.Equal(t, 2, chatRequests)
}

func TestCompatibleProvider_CreateBatch_InlineRequests(t *testing.T) {
	inlineReq := &core.BatchRequest{
		Endpoint:         "/v1/chat/completions",
		CompletionWindow: "24h",
		Requests: []core.BatchRequestItem{
			{CustomID: "a", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"gpt-4o-mini","messages":[]}`)},
			{CustomID: "b", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"gpt-4o-mini","messages":[]}`)},
		},
	}

	tests := []struct {
		name            string
		uploadStatus    int
		batchStatus     int
		wantErr         bool
		wantBatchCall   bool
		wantFileDeleted bool
	}{
		{name: "upload then create succeeds", uploadStatus: http.StatusOK, batchStatus: http.StatusOK, wantBatchCall: true},
		{name: "upload failure prevents batch create", uploadStatus: http.StatusInternalServerError, wantErr: true},
		{name: "create failure cleans up the uploaded file", uploadStatus: http.StatusOK, batchStatus: http.StatusBadRequest, wantErr: true, wantBatchCall: true, wantFileDeleted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/files" && r.Method == http.MethodPost:
					if tc.uploadStatus != http.StatusOK {
						w.WriteHeader(tc.uploadStatus)
						_, _ = w.Write([]byte(`{"error":{"message":"upload boom"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"file-abc","object":"file","purpose":"batch"}`))
				case r.URL.Path == "/batches" && r.Method == http.MethodPost:
					if tc.batchStatus != http.StatusOK {
						w.WriteHeader(tc.batchStatus)
						_, _ = w.Write([]byte(`{"error":{"message":"create boom"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"batch-up-1","object":"batch","status":"validating","input_file_id":"file-abc"}`))
				case r.URL.Path == "/files/file-abc" && r.Method == http.MethodDelete:
					_, _ = w.Write([]byte(`{"id":"file-abc","object":"file","deleted":true}`))
				default:
					assert.Failf(t, "unexpected upstream request", "%s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})

			provider := NewCompatibleProviderWithHTTPClient(
				"test-key",
				server.Client(),
				llmclient.Hooks{},
				CompatibleProviderConfig{ProviderName: "openai", BaseURL: server.URL},
			)

			resp, err := provider.CreateBatch(context.Background(), inlineReq)

			_, batchCalled := findRequest(capture, http.MethodPost, "/batches")
			assert.Equal(t, tc.wantBatchCall, batchCalled, "batch create call")
			_, fileDeleted := findRequest(capture, http.MethodDelete, "/files/file-abc")
			assert.Equal(t, tc.wantFileDeleted, fileDeleted, "uploaded file cleanup")

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "batch-up-1", resp.ID)

			upload, ok := findRequest(capture, http.MethodPost, "/files")
			require.True(t, ok, "inline requests must be uploaded as a file")
			uploaded := recordedMultipart(t, upload).files["file"]
			require.Len(t, uploaded, 1)
			lines := strings.Split(strings.TrimSpace(uploaded[0].data), "\n")
			require.Len(t, lines, 2)
			var first map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
			assert.Equal(t, "a", first["custom_id"])
			assert.Equal(t, "/v1/chat/completions", first["url"])
			assert.Equal(t, "POST", first["method"])

			create, ok := findRequest(capture, http.MethodPost, "/batches")
			require.True(t, ok)
			body := create.JSON(t)
			assert.Equal(t, "file-abc", body["input_file_id"])
			assert.NotContains(t, body, "requests", "inline requests leaked to the provider create body")
		})
	}
}
