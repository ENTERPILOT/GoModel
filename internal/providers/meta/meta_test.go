package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-meta",
			"created":1677652288,
			"model":"muse-spark-1.1",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("meta-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "muse-spark-1.1",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "muse-spark-1.1" {
		t.Fatalf("resp.Model = %q, want muse-spark-1.1", resp.Model)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Fatalf("resp.Usage = %+v, want total_tokens=4", resp.Usage)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer meta-key" {
		t.Fatalf("authorization = %q, want Bearer meta-key", gotAuth)
	}
	if gotBody["model"] != "muse-spark-1.1" {
		t.Fatalf("request model = %v, want muse-spark-1.1", gotBody["model"])
	}
}

func TestListModels_UsesModelsEndpoint(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{"id":"muse-spark-1.1","object":"model","created":1677652288,"owned_by":"meta"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("meta-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "muse-spark-1.1" {
		t.Fatalf("resp.Data = %+v, want one model muse-spark-1.1", resp.Data)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("meta-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("meta provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("meta provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("meta provider should not implement audio provider")
	}
}
