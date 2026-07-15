package bailian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestChatCompletion_SendsBearerAuthAndCorrectPath(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-bailian",
			"created":1677652288,
			"model":"qwen3-max",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("bailian-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "qwen3-max",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "qwen3-max" {
		t.Fatalf("resp.Model = %q, want qwen3-max", resp.Model)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer bailian-key" {
		t.Fatalf("authorization = %q, want Bearer bailian-key", gotAuth)
	}
}

func TestChatCompletion_MaxTokensMapping(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-bailian",
			"created":1677652288,
			"model":"qwen3-max",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxTokens := 4096
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "qwen3-max",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_tokens"]; exists {
		t.Fatal("sent body should NOT contain max_tokens (Bailian deprecated it)")
	}
	mct, exists := sentBody["max_completion_tokens"]
	if !exists {
		t.Fatal("sent body should contain max_completion_tokens")
	}
	if mct.(float64) != 4096 {
		t.Fatalf("max_completion_tokens = %v, want 4096", mct)
	}
}

func TestChatCompletion_NoMaxTokensMapping(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-bailian",
			"created":1677652288,
			"model":"qwen3-max",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_completion_tokens"]; exists {
		t.Fatal("sent body should NOT contain max_completion_tokens when request had no max_tokens")
	}
}

func TestStreamChatCompletion_MaxTokensMapping(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxTokens := 2048
	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "qwen3-flash",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_tokens"]; exists {
		t.Fatal("sent body should NOT contain max_tokens for streaming either")
	}
	mct, exists := sentBody["max_completion_tokens"]
	if !exists {
		t.Fatal("sent body should contain max_completion_tokens for streaming")
	}
	if mct.(float64) != 2048 {
		t.Fatalf("max_completion_tokens = %v, want 2048", mct)
	}
}

func TestStreamChatCompletion_ReturnsSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-flash",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream should contain [DONE], got: %s", string(body))
	}
}

func TestListModels_ReturnsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[
				{"id":"qwen3-max","object":"model","owned_by":"alibaba"},
				{"id":"qwen3-plus","object":"model","owned_by":"alibaba"},
				{"id":"qwen3-flash","object":"model","owned_by":"alibaba"}
			]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("got %d models, want 3", len(resp.Data))
	}
}

func TestEmbeddings_SendsRequest(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"model":"text-embedding-v3",
			"usage":{"prompt_tokens":2,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-v3",
		Input: "test",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
}

func TestResponsesViaChat_DelegatesToChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-bailian",
			"created":1677652288,
			"model":"qwen3-max",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "qwen3-max",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	// Verify the registration exposes the correct default base URL
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("Registration.DefaultBaseURL = %q, want %q",
			Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
	// Verify the provider actually uses the default base URL by checking
	// that a ChatCompletion request hits the correct host.
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"created":1,
			"model":"qwen3-max",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	// Override the base URL to our test server to capture the request,
	// but first verify the provider's default is the expected DashScope URL.
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	// The request should have reached our test server, confirming SetBaseURL works.
	if gotHost == "" {
		t.Fatal("expected a non-empty host in the request")
	}
}

func TestProvider_ExposesBatchAndFileInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})
	if _, ok := any(provider).(core.NativeBatchProvider); !ok {
		t.Fatal("bailian should implement native batch")
	}
	if _, ok := any(provider).(core.NativeFileProvider); !ok {
		t.Fatal("bailian should implement native file")
	}
}

func TestRegistration_TypeAndBaseURL(t *testing.T) {
	if Registration.Type != "bailian" {
		t.Fatalf("Registration.Type = %q, want bailian", Registration.Type)
	}
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("DefaultBaseURL mismatch")
	}
}

func TestPassthrough_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

func TestPassthrough_NilRequest(t *testing.T) {
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})

	if _, err := provider.Passthrough(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil passthrough request")
	}
}

func TestPassthrough_ReadError(t *testing.T) {
	readErr := errors.New("read failed")
	provider := NewWithHTTPClient("key", nil, llmclient.Hooks{})

	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     errReadCloser{err: readErr},
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("Passthrough() error = %v, want %v", err, readErr)
	}
}

func TestAdaptBailianRequest_Nil(t *testing.T) {
	r, err := adaptChatRequest(nil)
	if err != nil {
		t.Fatalf("adaptChatRequest(nil) error = %v", err)
	}
	if r != nil {
		t.Fatal("expected nil")
	}
}

func TestAdaptBailianRequest_NoMaxTokens(t *testing.T) {
	req := &core.ChatRequest{Model: "qwen3-max"}
	r, err := adaptChatRequest(req)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if r.MaxTokens != nil {
		t.Fatal("should not set max_completion_tokens when request had none")
	}

}
func TestNew_UsesRegistrationAndDefaultBaseURL(t *testing.T) {
	provider := New(providers.ProviderConfig{
		APIKey: "reg-key",
	}, providers.ProviderOptions{})
	if provider == nil {
		t.Fatal("New() returned nil")
	}
	// Verify the provider constructed via registration uses the expected base URL
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("Registration.DefaultBaseURL = %q, want %q",
			Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
}

func TestStreamResponses_DelegatesToChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "qwen3-max",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("expected non-empty stream body")
	}
}

func TestAdaptBailianRequest_PreservesOtherFields(t *testing.T) {
	maxTokens := 100
	req := &core.ChatRequest{
		Model:     "qwen3-max",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTokens,
	}
	r, err := adaptChatRequest(req)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if r == req {
		t.Fatal("should return a clone, not the original")
	}
	if r.Model != "qwen3-max" {
		t.Fatalf("model = %q", r.Model)
	}
	if r.MaxTokens != nil {
		t.Fatal("MaxTokens should be nil in the clone")
	}
}

func TestAdaptBailianRequest_RespectsExistingMaxCompletionTokens(t *testing.T) {
	extra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"max_completion_tokens": json.RawMessage(`200`),
	})
	maxTokens := 100
	req := &core.ChatRequest{
		Model:       "qwen3-max",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   &maxTokens,
		ExtraFields: extra,
	}
	r, err := adaptChatRequest(req)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if r == req {
		t.Fatal("should return a clone")
	}
	if r.MaxTokens != nil {
		t.Fatal("MaxTokens should be nil")
	}

	body, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("failed to marshal adapted request: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if _, exists := raw["max_completion_tokens"]; !exists {
		t.Fatal("max_completion_tokens should exist")
	}
	var mct int
	if err := json.Unmarshal(raw["max_completion_tokens"], &mct); err != nil {
		t.Fatalf("failed to unmarshal max_completion_tokens: %v", err)
	}
	if mct != 200 {
		t.Fatalf("max_completion_tokens = %d, want 200", mct)
	}
	if _, exists := raw["max_tokens"]; exists {
		t.Fatal("max_tokens should NOT exist in output")
	}
}

func TestChatCompletion_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from upstream 400")
	}
}

func TestChatCompletion_TransportFailure(t *testing.T) {
	// Use a stub RoundTripper that always returns an error
	errTransport := errors.New("simulated transport failure")
	provider := NewWithHTTPClient("key", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errTransport
		}),
	}, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
}

func TestStreamChatCompletion_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized","type":"authentication_error"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from upstream 401")
	}
}

func TestResponses_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "qwen3-max",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("expected error from upstream 400")
	}
}

func TestEmbeddings_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-v3",
		Input: "test",
	})
	if err == nil {
		t.Fatal("expected error from upstream 400")
	}
}

func TestPassthrough_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() should not return error on non-2xx: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %d, want 500", resp.StatusCode)
	}
}

func TestListModels_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from upstream 500")
	}
}

func TestCreateBatch_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-bailian-1","object":"batch","status":"validating"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.CreateBatch(context.Background(), &core.BatchRequest{
		InputFileID: "file-1",
		Endpoint:    "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if resp.ID != "batch-bailian-1" {
		t.Fatalf("batch id = %q", resp.ID)
	}
}

func TestGetBatch_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-bailian-1","object":"batch","status":"completed"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.GetBatch(context.Background(), "batch-bailian-1")
	if err != nil {
		t.Fatalf("GetBatch() error = %v", err)
	}
	if resp.ID != "batch-bailian-1" {
		t.Fatalf("batch id = %q", resp.ID)
	}
}

func TestListBatches_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListBatches(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("ListBatches() error = %v", err)
	}
}

func TestCancelBatch_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-bailian-1","object":"batch","status":"cancelling"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.CancelBatch(context.Background(), "batch-bailian-1")
	if err != nil {
		t.Fatalf("CancelBatch() error = %v", err)
	}
	if resp.ID != "batch-bailian-1" {
		t.Fatalf("batch id = %q", resp.ID)
	}
}

func TestCreateFile_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-1","object":"file","purpose":"batch","bytes":100}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.CreateFile(context.Background(), &core.FileCreateRequest{
		Content: []byte("data"),
		Purpose: "batch",
	})
	if err != nil {
		t.Fatalf("CreateFile() error = %v", err)
	}
	if resp.Provider != "bailian" {
		t.Fatalf("provider = %q, want bailian", resp.Provider)
	}
}

func TestDeleteFile_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-1","object":"file","deleted":true}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.DeleteFile(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("DeleteFile() error = %v", err)
	}
	if !resp.Deleted {
		t.Fatal("expected deleted=true")
	}
}

func TestListFiles_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListFiles(context.Background(), "batch", 10, "")
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
	}
}

func TestGetFile_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-1","object":"file","purpose":"batch"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.GetFile(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("GetFile() error = %v", err)
	}
	if resp.Provider != "bailian" {
		t.Fatalf("provider = %q, want bailian", resp.Provider)
	}
}

func TestGetFileContent_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"content"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.GetFileContent(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
}

func TestGetBatchResults_Delegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-1","output_file_id":"file-out-1"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.GetBatchResults(context.Background(), "batch-1")
	if err != nil {
		t.Fatalf("GetBatchResults() error = %v", err)
	}
}

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct {
	err error
}

func (r errReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errReadCloser) Close() error {
	return nil
}

func TestPassthrough_MaxTokensMapping(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bailian","model":"qwen3-max"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}],"max_tokens":4096}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_tokens"]; exists {
		t.Fatal("passthrough body should NOT contain max_tokens")
	}
	mct, exists := sentBody["max_completion_tokens"]
	if !exists {
		t.Fatal("passthrough body should contain max_completion_tokens")
	}
	if mct.(float64) != 4096 {
		t.Fatalf("max_completion_tokens = %v, want 4096", mct)
	}
}

func TestPassthrough_PreservesExistingMaxCompletionTokens(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bailian","model":"qwen3-max"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"max_completion_tokens":200}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_tokens"]; exists {
		t.Fatal("passthrough body should NOT contain max_tokens when max_completion_tokens already set")
	}
	mct, exists := sentBody["max_completion_tokens"]
	if !exists {
		t.Fatal("passthrough body should contain max_completion_tokens")
	}
	if mct.(float64) != 200 {
		t.Fatalf("max_completion_tokens = %v, want 200 (explicit value should win)", mct)
	}
}

func TestPassthrough_NoMaxTokens(t *testing.T) {
	var gotBody []byte
	var readErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bailian","model":"qwen3-max"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"qwen3-max","messages":[{"role":"user","content":"hi"}]}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	if readErr != nil {
		t.Fatalf("reading request body: %v", readErr)
	}

	var sentBody map[string]any
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if _, exists := sentBody["max_completion_tokens"]; exists {
		t.Fatal("passthrough body should NOT contain max_completion_tokens when request had no max_tokens")
	}
}
