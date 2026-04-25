package oracle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestListModels_ReturnsUpstreamInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openai.gpt-oss-120b","object":"model","owned_by":"oracle"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "openai.gpt-oss-120b" {
		t.Fatalf("unexpected models response: %+v", resp.Data)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{})

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "text-embedding-3-small"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
	if gatewayErr.Message != "oracle does not support embeddings" {
		t.Fatalf("gatewayErr.Message = %q, want oracle does not support embeddings", gatewayErr.Message)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("oracle provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("oracle provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.PassthroughProvider); ok {
		t.Fatal("oracle provider should not implement passthrough provider")
	}
}
