package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCompatibleProvider_AdaptChatRequest_RewritesBodyOnChatAndStream(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","model":"quirk-1","choices":[]}`))
	}))
	defer server.Close()

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
	if _, err := provider.ChatCompletion(context.Background(), original); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	stream, err := provider.StreamChatCompletion(context.Background(), original)
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	_, _ = io.ReadAll(stream)
	stream.Close()

	if len(bodies) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(bodies))
	}
	for i, body := range bodies {
		if !strings.Contains(body, `"adapted-user"`) {
			t.Fatalf("request %d body = %s, want adapted user", i, body)
		}
	}
	if original.User != "original-user" {
		t.Fatalf("original request mutated: User = %q", original.User)
	}
}

func TestCompatibleProvider_AdaptChatRequest_ErrorAborts(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
	}))
	defer server.Close()

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

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("ChatCompletion() error = nil, want adapter error")
	}
	if _, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("StreamChatCompletion() error = nil, want adapter error")
	}
	if upstreamCalled {
		t.Fatal("upstream called despite adapter error")
	}
}

func TestCompatibleProvider_ChatRequestHeaders_AppliedToChatOnly(t *testing.T) {
	headersByPath := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersByPath[r.URL.Path] = append(headersByPath[r.URL.Path], r.Header.Get("X-Conv-Id"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"resp","model":"m","choices":[]}`))
		}
	}))
	defer server.Close()

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

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	_, _ = io.ReadAll(stream)
	stream.Close()
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	for _, got := range headersByPath["/chat/completions"] {
		if got != "conv-m" {
			t.Fatalf("chat X-Conv-Id = %q, want conv-m", got)
		}
	}
	if len(headersByPath["/chat/completions"]) != 2 {
		t.Fatalf("chat requests = %d, want 2", len(headersByPath["/chat/completions"]))
	}
	for _, got := range headersByPath["/models"] {
		if got != "" {
			t.Fatalf("models X-Conv-Id = %q, want empty", got)
		}
	}
}
