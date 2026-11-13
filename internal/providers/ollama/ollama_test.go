package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestProvider builds a keyless provider pointed at baseURL.
func newTestProvider(baseURL string) *Provider {
	return New(providers.ProviderConfig{BaseURL: baseURL}, providertest.Options(llmclient.Hooks{})).(*Provider)
}

// Ollama's OpenAI-compatible surface translates Responses to chat completions
// and may run without an API key. Its embeddings use the native /api/embed
// endpoint, which the tests below cover.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "ollama",
		DefaultBaseURL: "http://localhost:11434/v1",
		SkipEmbeddings: true,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
	})
}

func TestNew(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
	}{
		{name: "with api key", apiKey: "test-api-key"},
		// Ollama doesn't require an API key
		{name: "without api key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newHTTPTestProvider(tt.apiKey, nil, llmclient.Hooks{})
			assert.Equal(t, tt.apiKey, provider.keys.Primary())
			assert.NotNil(t, provider.compat)
			assert.NotNil(t, provider.nativeClient)
		})
	}
}

// ListModels must stamp modes from native /api/show capabilities (embedding
// models get classified without a remote-registry entry), cache the results so
// repeat listings don't re-probe, and leave models unstamped when the probe
// fails so the ID heuristic can still apply.
func TestListModels_StampsShowCapabilities(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"llama3.2","object":"model","owned_by":"library"},
				{"id":"nomic-embed-text","object":"model","owned_by":"library"},
				{"id":"mystery-model","object":"model","owned_by":"library"}
			]}`))
		},
		"/api/show": func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Model {
			case "nomic-embed-text":
				_, _ = w.Write([]byte(`{"capabilities":["embedding"]}`))
			case "llama3.2":
				_, _ = w.Write([]byte(`{"capabilities":["completion","tools","vision","thinking"],
					"details":{"family":"llama","parameter_size":"3.2B"},
					"model_info":{"general.architecture":"llama","llama.context_length":131072,"llama.embedding_length":3072}}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		},
	})
	provider := newTestProvider(server.URL)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	byID := map[string]core.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	embed := byID["nomic-embed-text"]
	require.NotNil(t, embed.Metadata)
	assert.Equal(t, []string{"embedding"}, embed.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryEmbedding}, embed.Metadata.Categories)

	chat := byID["llama3.2"]
	require.NotNil(t, chat.Metadata)
	assert.Equal(t, []string{"chat"}, chat.Metadata.Modes)
	assert.Equal(t, "llama", chat.Metadata.Family)
	require.NotNil(t, chat.Metadata.ContextWindow)
	assert.Equal(t, 131072, *chat.Metadata.ContextWindow)
	assert.Equal(t, map[string]bool{"function_calling": true, "vision": true, "reasoning": true}, chat.Metadata.Capabilities)
	assert.Nil(t, embed.Metadata.Capabilities)
	assert.Nil(t, byID["mystery-model"].Metadata)

	// Second listing: successes served from cache, the failure re-probed.
	_, err = provider.ListModels(context.Background())
	require.NoError(t, err)

	showCalls := map[string]int{}
	for _, req := range capture.All() {
		if req.Path == "/api/show" {
			showCalls[req.JSON(t)["model"].(string)]++
		}
	}
	assert.Equal(t, map[string]int{"nomic-embed-text": 1, "llama3.2": 1, "mystery-model": 2}, showCalls, "successful probes must be cached")
}

func TestSetBaseURL_TrailingSlash(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"nomic-embed-text","embeddings":[[0.1,0.2]]}`)
	provider := newTestProvider(server.URL + "/v1/")

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/embed", capture.Last(t).Path)
}

func TestEmbeddings(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"model": "nomic-embed-text",
		"embeddings": [[0.1, 0.2, 0.3], [0.4, 0.5, 0.6]],
		"prompt_eval_count": 8
	}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: []string{"hello", "world"},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/api/embed", req.Path)
	assert.Equal(t, http.MethodPost, req.Method)
	var sent ollamaEmbedRequest
	require.NoError(t, json.Unmarshal(req.Body, &sent))
	assert.Equal(t, "nomic-embed-text", sent.Model)

	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, "nomic-embed-text", resp.Model)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "embedding", resp.Data[0].Object)

	var floats []float64
	require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &floats))
	assert.Len(t, floats, 3)
	assert.Equal(t, 1, resp.Data[1].Index)
	assert.Equal(t, 8, resp.Usage.PromptTokens)
	assert.Equal(t, 8, resp.Usage.TotalTokens)
}

func TestEmbeddings_ModelFallback(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"model": "",
		"embeddings": [[0.1]],
		"prompt_eval_count": 1
	}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "nomic-embed-text", resp.Model)
}

// TestEmbeddings_NoVectorsErrors guards the common misconfiguration where an
// OpenAI-compatible server (e.g. LM Studio) is registered as an "ollama"
// provider. Such servers answer the native /api/embed path with a 200 and an
// error body, which unmarshals into zero embeddings. The adapter must surface
// an error instead of returning an empty, OpenAI-shaped list.
func TestEmbeddings_NoVectorsErrors(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"error":"Unexpected endpoint or method. (POST /api/embed)"}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-nomic-embed-text-v1.5",
		Input: "hello world",
	})
	require.Error(t, err)
	require.Nil(t, resp)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, `"openai" or "vllm" provider`)
}

// TestEmbeddings_EmptyInputNoError ensures an empty input batch (an empty
// array/slice, including a typed []string{}) is not mistaken for the
// LM-Studio-as-ollama misconfiguration: zero vectors for an empty batch is a
// legitimate result, not a provider error.
func TestEmbeddings_EmptyInputNoError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"model":"nomic-embed-text","embeddings":[],"prompt_eval_count":0}`)
	provider := newTestProvider(server.URL + "/v1")

	for _, empty := range []any{[]any{}, []string{}} {
		resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
			Model: "nomic-embed-text",
			Input: empty,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Empty(t, resp.Data)
	}

	// Scalar/nil inputs are NOT empty batches: zero vectors must stay on the
	// loud-error path so a misconfigured OpenAI-compatible endpoint returning a
	// 200 error body for "" / null isn't silently swallowed as an empty list.
	for _, scalar := range []any{"", nil} {
		_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
			Model: "nomic-embed-text",
			Input: scalar,
		})
		assert.Error(t, err, "input %#v", scalar)
	}
}
