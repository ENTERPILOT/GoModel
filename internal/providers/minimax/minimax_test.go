package minimax

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

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-minimax",
			"created":1677652288,
			"model":"MiniMax-M2.7",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "MiniMax-M2.7",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "MiniMax-M2.7" {
		t.Fatalf("resp.Model = %q, want MiniMax-M2.7", resp.Model)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer minimax-key" {
		t.Fatalf("authorization = %q, want Bearer minimax-key", gotAuth)
	}
}

func TestChatCompletion_ClampsZeroTemperature(t *testing.T) {
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-minimax",
			"created":1677652288,
			"model":"MiniMax-M2.7",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	temp := 0.0
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "MiniMax-M2.7",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	// Verify the body sent to the server does not contain temperature=0
	bodyStr := string(gotBody)
	if strings.Contains(bodyStr, `"temperature":0`) {
		t.Fatalf("request body should not contain temperature=0, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"temperature":1`) {
		t.Fatalf("request body should contain temperature=1, got: %s", bodyStr)
	}
}

func TestProvider_DefaultBaseURL(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("minimax-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("minimax provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("minimax provider should not implement native file provider")
	}
}

func TestClampTemperature_NilRequest(t *testing.T) {
	result := clampTemperature(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestClampTemperature_NilTemperature(t *testing.T) {
	req := &core.ChatRequest{Model: "MiniMax-M2.7"}
	result := clampTemperature(req)
	if result.Temperature != nil {
		t.Fatal("expected nil temperature to remain nil")
	}
}

func TestClampTemperature_ZeroTemperature(t *testing.T) {
	temp := 0.0
	req := &core.ChatRequest{Model: "MiniMax-M2.7", Temperature: &temp}
	result := clampTemperature(req)
	if result.Temperature == nil {
		t.Fatal("expected non-nil temperature after clamping")
	}
	if *result.Temperature != defaultTemperature {
		t.Fatalf("expected temperature=%v after clamping zero, got %v", defaultTemperature, *result.Temperature)
	}
	// Original request should not be mutated
	if *req.Temperature != 0.0 {
		t.Fatal("original request should not be mutated")
	}
}

func TestClampTemperature_PositiveTemperature(t *testing.T) {
	temp := 0.7
	req := &core.ChatRequest{Model: "MiniMax-M2.7", Temperature: &temp}
	result := clampTemperature(req)
	if result != req {
		t.Fatal("expected same pointer for valid temperature")
	}
	if *result.Temperature != 0.7 {
		t.Fatalf("expected temperature=0.7, got %v", *result.Temperature)
	}
}
