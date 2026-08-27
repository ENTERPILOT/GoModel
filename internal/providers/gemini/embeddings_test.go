package gemini

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
			t.Errorf("path = %q, want /v1beta/models/gemini-embedding-001:batchEmbedContents", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"embeddings": [{"values": [0.1, 0.2]}, {"values": [0.3, 0.4]}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	dims := 128
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "gemini-embedding-001",
		Input:      []any{"first", "second"},
		Dimensions: &dims,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requests, ok := gotBody["requests"].([]any)
	if !ok || len(requests) != 2 {
		t.Fatalf("requests = %+v, want 2 entries", gotBody["requests"])
	}
	first, _ := requests[0].(map[string]any)
	if first["model"] != "models/gemini-embedding-001" {
		t.Errorf("request model = %v, want models/gemini-embedding-001", first["model"])
	}
	if first["outputDimensionality"] != float64(128) {
		t.Errorf("outputDimensionality = %v, want 128", first["outputDimensionality"])
	}
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "first" {
		t.Errorf("content parts = %+v, want single text part 'first'", parts)
	}

	if resp.Object != "list" || len(resp.Data) != 2 {
		t.Fatalf("response = %+v, want list with 2 embeddings", resp)
	}
	if resp.Model != "gemini-embedding-001" || resp.Provider != "gemini" {
		t.Errorf("model/provider = %q/%q, want gemini-embedding-001/gemini", resp.Model, resp.Provider)
	}
	var values []float64
	if err := json.Unmarshal(resp.Data[1].Embedding, &values); err != nil || len(values) != 2 || values[0] != 0.3 {
		t.Errorf("second embedding = %s, want [0.3, 0.4]", resp.Data[1].Embedding)
	}
	if resp.Data[1].Index != 1 || resp.Data[1].Object != "embedding" {
		t.Errorf("second entry = %+v, want index 1 object embedding", resp.Data[1])
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("usage = %+v, want zero (native API reports no usage)", resp.Usage)
	}
}

func TestNativeEmbeddings_Base64EncodingFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"embeddings": [{"values": [1.5, -2.0]}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:          "gemini-embedding-001",
		Input:          "hello",
		EncodingFormat: "base64",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var encoded string
	if err := json.Unmarshal(resp.Data[0].Embedding, &encoded); err != nil {
		t.Fatalf("embedding is not a base64 string: %s", resp.Data[0].Embedding)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 8 {
		t.Fatalf("embedding buffer = %d bytes (err %v), want 8", len(raw), err)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(raw[0:])); got != 1.5 {
		t.Errorf("first value = %v, want 1.5", got)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(raw[4:])); got != -2.0 {
		t.Errorf("second value = %v, want -2.0", got)
	}
}

func TestNativeEmbeddings_RejectsNonStringInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no upstream call expected for invalid input")
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []any{float64(1), float64(2)},
	})
	if err == nil {
		t.Fatal("expected token-array input to be rejected")
	}
}

func TestEmbeddings_CompatModeUsesOpenAIEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/embeddings" {
			t.Errorf("path = %q, want /v1beta/openai/embeddings", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object": "list", "data": [{"object": "embedding", "embedding": [0.5], "index": 0}], "model": "gemini-embedding-001", "usage": {"prompt_tokens": 3, "total_tokens": 3}}`))
	}))
	defer server.Close()

	p := newCompatTestProvider(t, server)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Usage.TotalTokens != 3 {
		t.Fatalf("response = %+v, want compat passthrough with usage", resp)
	}
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newNativeTestProvider(t, server)
			_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
				Model: "gemini-embedding-001",
				Input: []any{"first", "second"},
			})
			if err == nil || !strings.Contains(err.Error(), "embeddings for 2 inputs") {
				t.Fatalf("error = %v, want mismatched-count rejection", err)
			}
		})
	}
}
