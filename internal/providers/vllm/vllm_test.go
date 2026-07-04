package vllm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

func TestChatCompletion_UsesOptionalBearerAuthAndChatEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with api key", apiKey: "vllm-key", wantAuth: "Bearer vllm-key"},
		{name: "without api key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotAuth string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id":"chatcmpl-vllm",
					"created":1677652288,
					"model":"meta-llama/Llama-3.1-8B-Instruct",
					"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
				}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient(tt.apiKey, server.URL, server.Client(), llmclient.Hooks{}, nil, "")

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "meta-llama/Llama-3.1-8B-Instruct",
				Messages: []core.Message{
					{Role: "user", Content: "hi"},
				},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if resp.Model != "meta-llama/Llama-3.1-8B-Instruct" {
				t.Fatalf("resp.Model = %q, want meta-llama/Llama-3.1-8B-Instruct", resp.Model)
			}
			if gotPath != "/chat/completions" {
				t.Fatalf("path = %q, want /chat/completions", gotPath)
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("authorization = %q, want %q", gotAuth, tt.wantAuth)
			}
		})
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"model":"BAAI/bge-small-en-v1.5",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "BAAI/bge-small-en-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "BAAI/bge-small-en-v1.5" {
		t.Fatalf("resp.Model = %q, want BAAI/bge-small-en-v1.5", resp.Model)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
}

func TestProvider_ExposesPassthroughButNotOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{}, nil, "")

	if _, ok := any(provider).(core.PassthroughProvider); !ok {
		t.Fatal("vllm provider should implement passthrough provider")
	}
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("vllm provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("vllm provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("vllm provider should not implement native response lifecycle provider")
	}
}

func TestPassthrough_ForwardsProviderNativeEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("vllm-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/tokenize" {
		t.Fatalf("path = %q, want /tokenize", gotPath)
	}
	if gotAuth != "Bearer vllm-key" {
		t.Fatalf("authorization = %q, want Bearer vllm-key", gotAuth)
	}
}

func TestPassthrough_UsesRootForNativeEndpointsWhenBaseURLIncludesV1(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/tokenize" {
		t.Fatalf("path = %q, want /tokenize", gotPath)
	}
}

func TestPassthrough_UsesV1ForOpenAICompatibleEndpointsWhenBaseURLIncludesV1(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vllm",
			"created":1677652288,
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions",
		Body: io.NopCloser(strings.NewReader(`{
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"messages":[{"role":"user","content":"hi"}]
		}`)),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
}

// TestPassthrough_Root_AppliesStaticCustomHeaders verifies that custom upstream
// headers from HeaderOverridesConfig are applied to the non-v1 root passthrough
// client (separate from the OpenAI-compatible client).
func TestPassthrough_Root_AppliesStaticCustomHeaders(t *testing.T) {
	var gotRegion, gotTrace, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRegion = r.Header.Get("X-Provider-Region")
		gotTrace = r.Header.Get("X-Trace-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		CustomUpstreamHeaders: map[string]string{
			"X-Provider-Region": "us-east-1",
			"X-Trace-Id":        "trace-abc",
		},
	}
	provider := NewWithHTTPClient("vllm-key", server.URL+"/v1", server.Client(), llmclient.Hooks{}, cfg, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotRegion != "us-east-1" {
		t.Errorf("X-Provider-Region = %q, want us-east-1", gotRegion)
	}
	if gotTrace != "trace-abc" {
		t.Errorf("X-Trace-Id = %q, want trace-abc", gotTrace)
	}
	if gotAuth != "Bearer vllm-key" {
		t.Errorf("Authorization = %q, want Bearer vllm-key", gotAuth)
	}
}

// TestPassthrough_Root_StaticHeaders_BlocksCredentialAndInternal verifies that
// static custom headers on the non-v1 root passthrough client cannot leak
// credentials or the X-GoModel-User-Path internal alias.
func TestPassthrough_Root_StaticHeaders_BlocksCredentialAndInternal(t *testing.T) {
	var gotAuth, gotAPIKey, gotInternal, gotSafe string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotInternal = r.Header.Get("X-GoModel-User-Path")
		gotSafe = r.Header.Get("X-Safe")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		CustomUpstreamHeaders: map[string]string{
			"Authorization":       "Bearer leaked",
			"X-Api-Key":           "leaked-key",
			"X-GoModel-User-Path": "/internal",
			"X-Safe":              "ok",
		},
	}
	provider := NewWithHTTPClient("vllm-key", server.URL+"/v1", server.Client(), llmclient.Hooks{}, cfg, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	// Provider-set Authorization is still applied (setHeaders runs first).
	if gotAuth != "Bearer vllm-key" {
		t.Errorf("Authorization = %q, want Bearer vllm-key", gotAuth)
	}
	if gotAPIKey != "" {
		t.Errorf("X-Api-Key = %q, want empty (blocked)", gotAPIKey)
	}
	if gotInternal != "" {
		t.Errorf("X-GoModel-User-Path = %q, want empty (blocked)", gotInternal)
	}
	if gotSafe != "ok" {
		t.Errorf("X-Safe = %q, want ok", gotSafe)
	}
}

// TestPassthrough_Root_StaticHeaders_RespectsUserPathAlias verifies that the
// configured user-path alias is blocked on the non-v1 root passthrough client.
func TestPassthrough_Root_StaticHeaders_RespectsUserPathAlias(t *testing.T) {
	var gotAlias, gotOther string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAlias = r.Header.Get("X-My-Alias")
		gotOther = r.Header.Get("X-Other")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		CustomUpstreamHeaders: map[string]string{
			"X-My-Alias": "secret",
			"X-Other":    "ok",
		},
	}
	provider := NewWithHTTPClient("vllm-key", server.URL+"/v1", server.Client(), llmclient.Hooks{}, cfg, "X-My-Alias")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotAlias != "" {
		t.Errorf("X-My-Alias = %q, want empty (blocked by alias)", gotAlias)
	}
	if gotOther != "ok" {
		t.Errorf("X-Other = %q, want ok", gotOther)
	}
}

// TestPassthrough_Root_AppliesPassthroughUserHeaders verifies that passthrough
// mode applies user-supplied headers from context to the non-v1 root
// passthrough client while still blocking credentials and the hard-coded
// X-GoModel-User-Path floor.
func TestPassthrough_Root_AppliesPassthroughUserHeaders(t *testing.T) {
	var gotCustom, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-User-Custom")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		PassthroughUserHeaders: true,
	}
	provider := NewWithHTTPClient("vllm-key", server.URL+"/v1", server.Client(), llmclient.Hooks{}, cfg, "X-GoModel-User-Path")

	ctx := providers.WithPassthroughHeaders(context.Background(), http.Header{
		"X-User-Custom":       {"user-value"},
		"X-Other-Pass":        {"other-value"},
		"Authorization":       {"Bearer leaked"},
		"X-GoModel-User-Path": {"/internal"},
	})

	resp, err := provider.Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotCustom != "user-value" {
		t.Errorf("X-User-Custom = %q, want user-value", gotCustom)
	}
	// Auth must be the provider's, not the leaked user one.
	if gotAuth != "Bearer vllm-key" {
		t.Errorf("Authorization = %q, want Bearer vllm-key", gotAuth)
	}
}

// TestPassthrough_Root_PassthroughHeaders_AppliesSkipList verifies that the
// skip list filters passthrough headers on the non-v1 root passthrough client.
func TestPassthrough_Root_PassthroughHeaders_AppliesSkipList(t *testing.T) {
	var gotKept, gotSkipped string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKept = r.Header.Get("X-Keep-Me")
		gotSkipped = r.Header.Get("X-Skip-Me")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipHeaders:            []string{"X-Skip-Me"},
		SkipMode:               "skip",
	}
	provider := NewWithHTTPClient("vllm-key", server.URL+"/v1", server.Client(), llmclient.Hooks{}, cfg, "")

	ctx := providers.WithPassthroughHeaders(context.Background(), http.Header{
		"X-Keep-Me": {"keep"},
		"X-Skip-Me": {"drop"},
	})

	resp, err := provider.Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotKept != "keep" {
		t.Errorf("X-Keep-Me = %q, want keep", gotKept)
	}
	if gotSkipped != "" {
		t.Errorf("X-Skip-Me = %q, want empty (skipped)", gotSkipped)
	}
}

// TestNew_FactoryWiringPassesHeaderOverridesAndUserPathAlias verifies the
// factory constructor wires HeaderOverrides and UserPathHeader from
// ProviderOptions into both the OpenAI-compatible client and the non-v1 root
// passthrough client. We go through `New` (returns core.Provider) and assert to
// the concrete *Provider so we can call Passthrough on it.
func TestNew_FactoryWiringPassesHeaderOverridesAndUserPathAlias(t *testing.T) {
	var chatGotCustom, chatGotAuth string
	var rootGotCustom string

	mux := http.NewServeMux()
	// OpenAI-compatible (v1) client.
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		chatGotCustom = r.Header.Get("X-Provider-Custom")
		chatGotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vllm",
			"created":1677652288,
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	})
	// Root passthrough client (no /v1 prefix).
	mux.HandleFunc("/tokenize", func(w http.ResponseWriter, r *http.Request) {
		rootGotCustom = r.Header.Get("X-Provider-Custom")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := providers.ProviderConfig{
		Type:    "vllm",
		APIKey:  "vllm-key",
		BaseURL: server.URL + "/v1",
	}
	opts := providers.ProviderOptions{
		UserPathHeader: "X-GoModel-User-Path",
		HeaderOverrides: &providers.HeaderOverridesConfig{
			CustomUpstreamHeaders: map[string]string{
				"X-Provider-Custom": "factory-value",
			},
		},
	}

	provider, ok := New(cfg, opts).(*Provider)
	if !ok {
		t.Fatalf("New() did not return *vllm.Provider, got %T", provider)
	}

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen2.5-0.5B-Instruct",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if chatGotCustom != "factory-value" {
		t.Errorf("chat X-Provider-Custom = %q, want factory-value", chatGotCustom)
	}
	if chatGotAuth != "Bearer vllm-key" {
		t.Errorf("chat Authorization = %q, want Bearer vllm-key", chatGotAuth)
	}

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if rootGotCustom != "factory-value" {
		t.Errorf("root X-Provider-Custom = %q, want factory-value", rootGotCustom)
	}
}
