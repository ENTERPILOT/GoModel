package oracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestListModels_FallsBackToConfiguredModelsWhenUpstreamFails(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{}, []string{
		"openai.gpt-oss-120b",
		"xai.grok-3",
	})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(resp.Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].ID != "openai.gpt-oss-120b" {
		t.Fatalf("resp.Data[0].ID = %q, want openai.gpt-oss-120b", resp.Data[0].ID)
	}
	if resp.Data[1].ID != "xai.grok-3" {
		t.Fatalf("resp.Data[1].ID = %q, want xai.grok-3", resp.Data[1].ID)
	}
	for i, model := range resp.Data {
		if model.Object != "model" {
			t.Fatalf("resp.Data[%d].Object = %q, want model", i, model.Object)
		}
		if model.OwnedBy != "oracle" {
			t.Fatalf("resp.Data[%d].OwnedBy = %q, want oracle", i, model.OwnedBy)
		}
	}
}

func TestListModels_FiltersUpstreamModelsAndAddsMissingConfiguredModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "xai.grok-3", Object: "model", OwnedBy: "oracle", Created: 123},
				{ID: "ignore-me", Object: "model", OwnedBy: "oracle", Created: 456},
			},
		})
	}))
	defer server.Close()

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{}, []string{
		"openai.gpt-oss-120b",
		"xai.grok-3",
	})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(resp.Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].ID != "openai.gpt-oss-120b" {
		t.Fatalf("resp.Data[0].ID = %q, want openai.gpt-oss-120b", resp.Data[0].ID)
	}
	if resp.Data[1].ID != "xai.grok-3" {
		t.Fatalf("resp.Data[1].ID = %q, want xai.grok-3", resp.Data[1].ID)
	}
	if resp.Data[1].Created != 123 {
		t.Fatalf("resp.Data[1].Created = %d, want 123", resp.Data[1].Created)
	}
}

func TestListModels_ReturnsUpstreamInventoryWhenNoConfiguredModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openai.gpt-oss-120b","object":"model","owned_by":"oracle"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{}, nil)
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "openai.gpt-oss-120b" {
		t.Fatalf("unexpected models response: %+v", resp.Data)
	}
}

func TestListModels_ReturnsActionableErrorWhenUpstreamFailsWithoutConfiguredModels(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{}, nil)
	provider.SetBaseURL(server.URL)

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
	if gatewayErr.Provider != "oracle" {
		t.Fatalf("gatewayErr.Provider = %q, want oracle", gatewayErr.Provider)
	}
	if !strings.Contains(err.Error(), "add providers.<name>.models in config.yaml") {
		t.Fatalf("err = %q, want mention of providers.<name>.models in config.yaml", err)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{}, nil)

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
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{}, nil)

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

func TestNormalizeConfiguredModels(t *testing.T) {
	got := normalizeConfiguredModels([]string{
		" openai.gpt-oss-120b ",
		"",
		"xai.grok-3",
		"openai.gpt-oss-120b",
	})

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0] != "openai.gpt-oss-120b" || got[1] != "xai.grok-3" {
		t.Fatalf("got = %v, want [openai.gpt-oss-120b xai.grok-3]", got)
	}
}

func TestNormalizeConfiguredModels_AllEmpty(t *testing.T) {
	got := normalizeConfiguredModels([]string{"", "   ", ""})
	if got != nil {
		t.Fatalf("got = %v, want nil", got)
	}
}
