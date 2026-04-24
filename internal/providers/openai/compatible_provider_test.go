package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestCompatibleProvider_ListModels_ReturnsUpstreamOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","owned_by":"openai"}]}`))
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "upstream-only",
			BaseURL:      server.URL,
		},
	)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "gpt-4o" {
		t.Fatalf("unexpected models: %+v", resp.Data)
	}
}

func TestCompatibleProvider_ListModels_ReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "test-provider",
			BaseURL:      server.URL,
		},
	)

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error when upstream fails, got nil")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider && gatewayErr.Type != core.ErrorTypeNotFound {
		t.Errorf("gatewayErr.Type = %q, want provider_error or not_found_error", gatewayErr.Type)
	}
}
