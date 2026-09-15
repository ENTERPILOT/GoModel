package azure

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chatCompletionJSON = `{
	"id": "chatcmpl-123",
	"object": "chat.completion",
	"created": 1677652288,
	"model": "gpt-4o",
	"choices": [{
		"index": 0,
		"message": {"role": "assistant", "content": "hello"},
		"finish_reason": "stop"
	}]
}`

// newTestProvider points a provider at the given upstream base URL.
func newTestProvider(client *http.Client, baseURL string) *Provider {
	provider := NewWithHTTPClient("test-api-key", client, llmclient.Hooks{})
	provider.SetBaseURL(baseURL)
	return provider
}

func TestChatCompletion_UsesAzureAuthAndDefaultAPIVersion(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.Client(), server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/chat/completions", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("api-key"))
	assert.Empty(t, sent.Header.Get("Authorization"))
	assert.Equal(t, defaultAPIVersion, sent.Query.Get("api-version"))
}

func TestSetAPIVersion_OverridesDefault(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.Client(), server.URL)
	provider.SetAPIVersion("2025-04-01-preview")

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "2025-04-01-preview", capture.Last(t).Query.Get("api-version"))
}

func TestListModels_UsesAzureOpenAIPath(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	provider := newTestProvider(server.Client(), server.URL)

	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/openai/models", sent.Path)
	assert.Equal(t, defaultAPIVersion, sent.Query.Get("api-version"))
}

// batchEndpointCases exercises every batch surface; each case names the Azure
// path and method the shared OpenAI batch adapter must produce.
func batchEndpointCases() []struct {
	name         string
	call         func(*Provider) error
	wantPath     string
	wantMethod   string
	responseBody string
} {
	const batchJSON = `{
		"id":"batch_123",
		"object":"batch",
		"endpoint":"/v1/chat/completions",
		"status":"validating",
		"created_at":1677652288,
		"request_counts":{"total":1,"completed":0,"failed":0}
	}`
	return []struct {
		name         string
		call         func(*Provider) error
		wantPath     string
		wantMethod   string
		responseBody string
	}{
		{
			name: "create",
			call: func(p *Provider) error {
				_, err := p.CreateBatch(context.Background(), &core.BatchRequest{
					InputFileID:      "file-123",
					Endpoint:         "/v1/chat/completions",
					CompletionWindow: "24h",
				})
				return err
			},
			wantPath:     "/openai/batches",
			wantMethod:   http.MethodPost,
			responseBody: batchJSON,
		},
		{
			name: "get",
			call: func(p *Provider) error {
				_, err := p.GetBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:     "/openai/batches/batch_123",
			wantMethod:   http.MethodGet,
			responseBody: batchJSON,
		},
		{
			name: "list",
			call: func(p *Provider) error {
				_, err := p.ListBatches(context.Background(), 10, "batch_122")
				return err
			},
			wantPath:     "/openai/batches",
			wantMethod:   http.MethodGet,
			responseBody: `{"object":"list","data":[],"has_more":false}`,
		},
		{
			name: "cancel",
			call: func(p *Provider) error {
				_, err := p.CancelBatch(context.Background(), "batch_123")
				return err
			},
			wantPath:     "/openai/batches/batch_123/cancel",
			wantMethod:   http.MethodPost,
			responseBody: batchJSON,
		},
	}
}

func TestBatchEndpoints_UseAzureOpenAIPaths(t *testing.T) {
	for _, tt := range batchEndpointCases() {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, tt.responseBody)
			provider := newTestProvider(server.Client(), server.URL)

			require.NoError(t, tt.call(provider))

			sent := capture.Last(t)
			assert.Equal(t, tt.wantPath, sent.Path)
			assert.Equal(t, tt.wantMethod, sent.Method)
			assert.Equal(t, defaultAPIVersion, sent.Query.Get("api-version"))
		})
	}
}

func TestListModels_UsesAzureResourceRootForDeploymentScopedBaseURL(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	provider := newTestProvider(server.Client(), server.URL+"/openai/deployments/gpt-4o")

	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/openai/models", capture.Last(t).Path)
}

func TestBatchEndpoints_UseAzureResourceRootForDeploymentScopedBaseURL(t *testing.T) {
	for _, tt := range batchEndpointCases() {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, tt.responseBody)
			provider := newTestProvider(server.Client(), server.URL+"/openai/deployments/gpt-4o")

			require.NoError(t, tt.call(provider))

			sent := capture.Last(t)
			assert.Equal(t, tt.wantPath, sent.Path)
			assert.Equal(t, tt.wantMethod, sent.Method)
		})
	}
}

func TestGetBatchResults_UsesAzureResourceRootForDeploymentScopedBaseURL(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/openai/batches/batch_1": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"batch_1","status":"completed","output_file_id":"file_1","endpoint":"/v1/chat/completions"}`)
		},
		"/openai/files/file_1/content": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/jsonl")
			_, _ = io.WriteString(w, `{"custom_id":"ok-1","response":{"status_code":200,"url":"/v1/chat/completions","body":{"id":"resp-1","model":"gpt-4o-mini"}}}`)
		},
	})
	provider := newTestProvider(server.Client(), server.URL+"/openai/deployments/gpt-4o")

	resp, err := provider.GetBatchResults(context.Background(), "batch_1")
	require.NoError(t, err)
	assert.Equal(t, "batch_1", resp.BatchID)

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, "/openai/batches/batch_1", requests[0].Path)
	assert.Equal(t, "/openai/files/file_1/content", requests[1].Path)
	for i, sent := range requests {
		assert.Equal(t, defaultAPIVersion, sent.Query.Get("api-version"), "request %d", i)
	}
}
