package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "deepseek-v4-pro" {
		t.Fatalf("resp.Model = %q, want deepseek-v4-pro", resp.Model)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer deepseek-key" {
		t.Fatalf("authorization = %q, want Bearer deepseek-key", gotAuth)
	}
}

func TestChatCompletion_MapsReasoningToDeepSeekReasoningEffort(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "deepseek-v4-pro",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotBody["reasoning"] != nil {
		t.Fatalf("request body should not include nested reasoning, got %#v", gotBody["reasoning"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", gotBody["reasoning_effort"])
	}
}

func TestResponses_TranslatesToChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"translated"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")
	maxOutputTokens := 64

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:           "deepseek-v4-pro",
		Input:           "Reply with exactly ok",
		MaxOutputTokens: &maxOutputTokens,
		Reasoning:       &core.Reasoning{Effort: "xhigh"},
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotBody["max_output_tokens"] != nil {
		t.Fatalf("request body should not include max_output_tokens, got %#v", gotBody["max_output_tokens"])
	}
	if gotBody["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens = %#v, want 64", gotBody["max_tokens"])
	}
	if gotBody["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", gotBody["reasoning_effort"])
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want one chat message", gotBody["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if message["role"] != "user" || message["content"] != "Reply with exactly ok" {
		t.Fatalf("message = %#v, want converted user message", message)
	}
	if resp.Object != "response" || resp.Status != "completed" {
		t.Fatalf("response metadata = object %q status %q, want response/completed", resp.Object, resp.Status)
	}
	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "translated" {
		t.Fatalf("unexpected responses output: %+v", resp.Output)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want total_tokens=5", resp.Usage)
	}
}

func TestStreamResponses_TranslatesToChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-deepseek\",\"object\":\"chat.completion.chunk\",\"created\":1677652288,\"model\":\"deepseek-v4-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "deepseek-v4-pro",
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream = %#v, want true", gotBody["stream"])
	}
	raw := string(body)
	if !strings.Contains(raw, "response.output_text.delta") || !strings.Contains(raw, "data: [DONE]") {
		t.Fatalf("converted stream missing responses events or done marker: %s", raw)
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := map[string]string{
		"low":    "high",
		"medium": "high",
		"high":   "high",
		"xhigh":  "max",
		"max":    "max",
		"custom": "custom",
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			if got := normalizeReasoningEffort(input); got != expected {
				t.Fatalf("normalizeReasoningEffort(%q) = %q, want %q", input, got, expected)
			}
		})
	}
}

func TestProvider_DoesNotExposeOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("deepseek provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("deepseek provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("deepseek provider should not implement native response lifecycle provider")
	}
}

func TestPassthrough_ForwardsRequestWithBearerAuth(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"fim_completion","choices":[{"text":"world"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	body := strings.NewReader(`{"model":"deepseek-v4-pro","prompt":"hello "}`)
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(body),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if gotPath != "/beta/completions" {
		t.Fatalf("path = %q, want /beta/completions", gotPath)
	}
	if gotAuth != "Bearer deepseek-key" {
		t.Fatalf("authorization = %q, want Bearer deepseek-key", gotAuth)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(string(gotBody), "deepseek-v4-pro") {
		t.Fatalf("body = %q, want body containing deepseek-v4-pro", gotBody)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPassthrough_NilRequest_ReturnsError(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")
	_, err := provider.Passthrough(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil passthrough request, got nil")
	}
}

func TestPassthrough_ForwardsRequestHeaders(t *testing.T) {
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
}

func TestPassthrough_PreservesNon2xxStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

func TestPassthrough_ForwardsQueryString(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "/beta/completions?stream=true",
		Body:     io.NopCloser(strings.NewReader(``)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if gotPath != "/beta/completions?stream=true" {
		t.Fatalf("path = %q, want /beta/completions?stream=true", gotPath)
	}
}

func TestPassthrough_PreservesResponseBody(t *testing.T) {
	const upstreamBody = `{"error":{"message":"rate_limit_exceeded","type":"rate_limit_error"}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != upstreamBody {
		t.Fatalf("response body = %q, want %q", string(body), upstreamBody)
	}
}

func TestProvider_ImplementsPassthroughProvider(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")
	var _ core.PassthroughProvider = provider
}

func TestResponses_NilRequest_ReturnsError(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")
	_, err := provider.Responses(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil Responses request, got nil")
	}
}

func TestStreamResponses_NilRequest_ReturnsError(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")
	_, err := provider.StreamResponses(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil StreamResponses request, got nil")
	}
}

func TestEmbeddings_ReturnsUnsupported(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{}, nil, "")

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "embedding-model", Input: "hi"})
	if err == nil {
		t.Fatal("expected unsupported embeddings error, got nil")
	}
}

func TestChatCompletion_AppliesStaticCustomHeaders(t *testing.T) {
	var gotRegion, gotTrace string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRegion = r.Header.Get("X-Provider-Region")
		gotTrace = r.Header.Get("X-Trace-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		CustomUpstreamHeaders: map[string]string{
			"X-Provider-Region": "us-east-1",
			"X-Trace-Id":        "trace-abc",
		},
	}
	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, cfg, "")

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotRegion != "us-east-1" {
		t.Errorf("X-Provider-Region = %q, want us-east-1", gotRegion)
	}
	if gotTrace != "trace-abc" {
		t.Errorf("X-Trace-Id = %q, want trace-abc", gotTrace)
	}
}

func TestChatCompletion_StaticHeaders_BlocksCredentialAndInternal(t *testing.T) {
	var gotAuth, gotAPIKey, gotInternal, gotSafe string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotInternal = r.Header.Get("X-GoModel-User-Path")
		gotSafe = r.Header.Get("X-Safe")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
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
	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, cfg, "")

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	// Provider-set Authorization is still applied (setHeaders runs first).
	if gotAuth != "Bearer deepseek-key" {
		t.Errorf("Authorization = %q, want Bearer deepseek-key", gotAuth)
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

func TestChatCompletion_StaticHeaders_RespectsUserPathAlias(t *testing.T) {
	var gotAlias, gotOther string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAlias = r.Header.Get("X-My-Alias")
		gotOther = r.Header.Get("X-Other")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		CustomUpstreamHeaders: map[string]string{
			"X-My-Alias": "secret",
			"X-Other":    "ok",
		},
	}
	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, cfg, "X-My-Alias")

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotAlias != "" {
		t.Errorf("X-My-Alias = %q, want empty (blocked by alias)", gotAlias)
	}
	if gotOther != "ok" {
		t.Errorf("X-Other = %q, want ok", gotOther)
	}
}

func TestChatCompletion_PassthroughUserHeaders(t *testing.T) {
	var gotCustom, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-User-Custom")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		PassthroughUserHeaders: true,
	}
	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, cfg, "X-GoModel-User-Path")

	ctx := providers.WithPassthroughHeaders(context.Background(), http.Header{
		"X-User-Custom":  {"user-value"},
		"X-Other-Pass":   {"other-value"},
		"X-Skip-Me":      {"nope"},
		"Authorization":  {"Bearer leaked"},
		"X-GoModel-User-Path": {"/internal"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotCustom != "user-value" {
		t.Errorf("X-User-Custom = %q, want user-value", gotCustom)
	}
	// Auth must be the provider's, not the leaked user one.
	if gotAuth != "Bearer deepseek-key" {
		t.Errorf("Authorization = %q, want Bearer deepseek-key", gotAuth)
	}
}

func TestChatCompletion_PassthroughHeaders_AppliesSkipList(t *testing.T) {
	var gotKept, gotSkipped string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKept = r.Header.Get("X-Keep-Me")
		gotSkipped = r.Header.Get("X-Skip-Me")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	cfg := &providers.HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipHeaders:            []string{"X-Skip-Me"},
		SkipMode:               "skip",
	}
	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{}, cfg, "")

	ctx := providers.WithPassthroughHeaders(context.Background(), http.Header{
		"X-Keep-Me": {"keep"},
		"X-Skip-Me": {"drop"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotKept != "keep" {
		t.Errorf("X-Keep-Me = %q, want keep", gotKept)
	}
	if gotSkipped != "" {
		t.Errorf("X-Skip-Me = %q, want empty (skipped)", gotSkipped)
	}
}

func TestNew_FactoryWiringPassesHeaderOverridesAndUserPathAlias(t *testing.T) {
	var gotCustom, gotAlias string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Provider-Custom")
		gotAlias = r.Header.Get("X-GoModel-User-Path")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1677652288,
			"model":"deepseek-v4-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	cfg := providers.ProviderConfig{
		Type:    "deepseek",
		APIKey:  "deepseek-key",
		BaseURL: server.URL,
		CustomUpstreamHeaders: map[string]string{
			"X-Provider-Custom": "factory-value",
		},
	}
	opts := providers.ProviderOptions{
		UserPathHeader: "X-GoModel-User-Path",
		HeaderOverrides: &providers.HeaderOverridesConfig{
			CustomUpstreamHeaders: map[string]string{
				"X-Provider-Custom": "factory-value",
			},
		},
	}

	provider := New(cfg, opts)
	ctx := providers.WithPassthroughHeaders(context.Background(), http.Header{
		"X-GoModel-User-Path": {"/v1/chat/completions"},
	})

	if _, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotCustom != "factory-value" {
		t.Errorf("X-Provider-Custom = %q, want factory-value", gotCustom)
	}
	// X-GoModel-User-Path is hard-coded blocked; alias test ensures it doesn't leak.
	if gotAlias != "" {
		t.Errorf("X-GoModel-User-Path = %q, want empty (blocked)", gotAlias)
	}
}
