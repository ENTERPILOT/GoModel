package gemini

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNativeTestProvider builds an AI Studio provider in native mode pointed at
// the test server; the native base derives from the /openai suffix.
func newNativeTestProvider(t *testing.T, server *httptest.Server) *Provider {
	t.Helper()
	t.Setenv(useNativeAPIEnvVar, "true")
	p := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL + "/v1beta/openai")
	return p
}

func newCompatTestProvider(t *testing.T, server *httptest.Server) *Provider {
	t.Helper()
	t.Setenv(useNativeAPIEnvVar, "false")
	p := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	p.SetBaseURL(server.URL + "/v1beta/openai")
	return p
}

func TestNativeEmbeddings_BatchEmbedContents(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"embeddings": [{"values": [0.1, 0.2]}, {"values": [0.3, 0.4]}]}`)

	p := newNativeTestProvider(t, server)
	dims := 128
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "gemini-embedding-001",
		Input:      []any{"first", "second"},
		Dimensions: &dims,
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models/gemini-embedding-001:batchEmbedContents", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))

	requests, ok := sent.JSON(t)["requests"].([]any)
	require.True(t, ok, "requests missing from batch body")
	require.Len(t, requests, 2)

	first, _ := requests[0].(map[string]any)
	assert.Equal(t, "models/gemini-embedding-001", first["model"])
	assert.Equal(t, float64(128), first["outputDimensionality"])

	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	require.Len(t, parts, 1)
	assert.Equal(t, "first", parts[0].(map[string]any)["text"])

	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, "gemini-embedding-001", resp.Model)
	assert.Equal(t, "gemini", resp.Provider)
	require.Len(t, resp.Data, 2)

	var values []float64
	require.NoError(t, json.Unmarshal(resp.Data[1].Embedding, &values))
	assert.Equal(t, []float64{0.3, 0.4}, values)
	assert.Equal(t, 1, resp.Data[1].Index)
	assert.Equal(t, "embedding", resp.Data[1].Object)
	assert.Equal(t, 0, resp.Usage.TotalTokens, "native API reports no usage")
}

func TestNativeEmbeddings_Base64EncodingFormat(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"embeddings": [{"values": [1.5, -2.0]}]}`)

	p := newNativeTestProvider(t, server)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:          "gemini-embedding-001",
		Input:          "hello",
		EncodingFormat: "base64",
	})
	require.NoError(t, err)

	var encoded string
	require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &encoded), "embedding is not a base64 string: %s", resp.Data[0].Embedding)

	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Len(t, raw, 8)
	assert.Equal(t, float32(1.5), math.Float32frombits(binary.LittleEndian.Uint32(raw[0:])))
	assert.Equal(t, float32(-2.0), math.Float32frombits(binary.LittleEndian.Uint32(raw[4:])))
}

func TestNativeEmbeddings_RejectsNonStringInput(t *testing.T) {
	server, capture := providertest.Server(t, nil)

	p := newNativeTestProvider(t, server)
	_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []any{float64(1), float64(2)},
	})
	require.Error(t, err)
	assert.Zero(t, capture.Count(), "no upstream call expected for invalid input")
}

func TestEmbeddings_CompatModeUsesOpenAIEndpoint(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object": "list", "data": [{"object": "embedding", "embedding": [0.5], "index": 0}], "model": "gemini-embedding-001", "usage": {"prompt_tokens": 3, "total_tokens": 3}}`)

	p := newCompatTestProvider(t, server)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "/v1beta/openai/embeddings", capture.Last(t).Path)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, 3, resp.Usage.TotalTokens, "compat mode passes usage through")
}

func TestNativeEmbeddings_RejectsMismatchedCount(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "short", body: `{"embeddings": [{"values": [0.1]}]}`},
		{name: "surplus", body: `{"embeddings": [{"values": [0.1]}, {"values": [0.2]}, {"values": [0.3]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, tt.body)

			p := newNativeTestProvider(t, server)
			_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
				Model: "gemini-embedding-001",
				Input: []any{"first", "second"},
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "embeddings for 2 inputs")
		})
	}
}
