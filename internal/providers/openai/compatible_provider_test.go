package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
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

func TestCompatibleProvider_ListModels_DefaultsMissingObjectFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openrouter/model","object":"","owned_by":"openrouter"}]}`))
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{
			ProviderName: "openrouter",
			BaseURL:      server.URL,
		},
	)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if resp.Object != "list" {
		t.Fatalf("response object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("model count = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "model" {
		t.Fatalf("model object = %q, want model", resp.Data[0].Object)
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

func TestCompatibleProvider_AppliesHeaderOverrides(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	customHeaders := map[string]string{
		"X-Custom-Header": "custom-value",
		"X-Another-Header": "another-value",
	}

	opts := providers.ProviderOptions{
		HeaderOverrides: &providers.HeaderOverridesConfig{
			CustomUpstreamHeaders: customHeaders,
			SkipHeaders:           []string{"X-Skipped-Header"},
		},
		UserPathHeader: "X-User-Path",
	}

	provider := NewCompatibleProvider(
		"test-key",
		opts,
		CompatibleProviderConfig{
			ProviderName: "test-provider",
			BaseURL:      server.URL,
		},
	)

	_, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	// Verify custom headers are applied
	if capturedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want custom-value", capturedHeaders.Get("X-Custom-Header"))
	}
	if capturedHeaders.Get("X-Another-Header") != "another-value" {
		t.Errorf("X-Another-Header = %q, want another-value", capturedHeaders.Get("X-Another-Header"))
	}

	// Verify internal headers are blocked
	if capturedHeaders.Get("X-GoModel-User-Path") != "" {
		t.Errorf("X-GoModel-User-Path should be blocked, got %q", capturedHeaders.Get("X-GoModel-User-Path"))
	}
	if capturedHeaders.Get("Authorization") != "" {
		t.Errorf("Authorization header should be blocked, got %q", capturedHeaders.Get("Authorization"))
	}
}

// TestCompatibleProvider_HeaderOverridesViaWithHTTPClient exercises the
// NewCompatibleProviderWithHTTPClient path: the custom HTTP client is
// honored while the HeaderOverrides config still drives custom headers
// and the hard-coded block list still strips credential headers.
func TestCompatibleProvider_HeaderOverridesViaWithHTTPClient(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","object":"model","owned_by":"x"}]}`))
	}))
	defer server.Close()

	cfg := CompatibleProviderConfig{
		ProviderName: "with-http-client",
		BaseURL:      server.URL,
		HeaderOverrides: &providers.HeaderOverridesConfig{
			CustomUpstreamHeaders: map[string]string{
				"X-Custom-Header":     "custom-value",
				"X-Another-Header":    "another-value",
				"Authorization":       "should-be-blocked", // hard-coded credential block
				"X-GoModel-User-Path": "should-be-blocked", // hard-coded internal block
			},
		},
	}

	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		cfg,
	)

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	if got := capturedHeaders.Get("X-Custom-Header"); got != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want custom-value", got)
	}
	if got := capturedHeaders.Get("X-Another-Header"); got != "another-value" {
		t.Errorf("X-Another-Header = %q, want another-value", got)
	}
	if got := capturedHeaders.Get("X-GoModel-User-Path"); got != "" {
		t.Errorf("X-GoModel-User-Path = %q, want empty (blocked)", got)
	}
	// Authorization set via custom header map must be skipped by the static
	// block list; the only Authorization on the wire is the one set by
	// SetHeaders (Bearer ...), and the custom value must not be appended.
	for _, v := range capturedHeaders.Values("Authorization") {
		if strings.HasPrefix(v, "should-be-blocked") {
			t.Errorf("Authorization leaked custom value %q", v)
		}
	}
}

// TestCompatibleProvider_HeaderOverridesViaChatCompatible proves the
// ChatCompatible → CompatibleProvider delegation path receives
// HeaderOverrides. This is the same path Kimi and other ChatCompatible
// providers use, so a passing test here implies those providers will
// forward custom headers as well.
func TestCompatibleProvider_HeaderOverridesViaChatCompatible(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","object":"model","owned_by":"x"}]}`))
	}))
	defer server.Close()

	opts := providers.ProviderOptions{
		HeaderOverrides: &providers.HeaderOverridesConfig{
			CustomUpstreamHeaders: map[string]string{
				"X-Custom-Header":     "custom-value",
				"X-Another-Header":    "another-value",
				"Authorization":       "should-be-blocked", // hard-coded credential block
				"X-GoModel-User-Path": "should-be-blocked", // hard-coded internal block
			},
		},
		UserPathHeader: "X-User-Path",
	}

	chat := NewChatCompatible(
		"test-key",
		opts,
		CompatibleProviderConfig{
			ProviderName: "chat-compatible",
			BaseURL:      server.URL,
		},
	)

	if _, err := chat.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	if got := capturedHeaders.Get("X-Custom-Header"); got != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want custom-value", got)
	}
	if got := capturedHeaders.Get("X-Another-Header"); got != "another-value" {
		t.Errorf("X-Another-Header = %q, want another-value", got)
	}
	// Hard-coded blocks must be skipped.
	if got := capturedHeaders.Get("X-GoModel-User-Path"); got != "" {
		t.Errorf("X-GoModel-User-Path = %q, want empty (blocked)", got)
	}
	for _, v := range capturedHeaders.Values("Authorization") {
		if strings.HasPrefix(v, "should-be-blocked") {
			t.Errorf("Authorization leaked custom value %q", v)
		}
	}
	// Bearer auth must still be present (set by the default SetHeaders).
	if got := capturedHeaders.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("Authorization = %q, want Bearer prefix", got)
	}
}
