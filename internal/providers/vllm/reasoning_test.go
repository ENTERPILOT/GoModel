package vllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_RenamesLegacyReasoningContentOnAssistantMessages(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vllm",
			"created":1,
			"model":"Qwen3.8-27B",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	var req core.ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"Qwen3.8-27B",
		"messages":[
			{"role":"user","content":"what is the median life-expectancy of a cat"},
			{"role":"assistant","content":"12-15 years","reasoning_content":"the user wants a quick factual answer"},
			{"role":"user","content":"and a dog's?"}
		]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.ChatCompletion(context.Background(), &req); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	messages, _ := gotBody["messages"].([]any)
	assistantMsg, _ := messages[1].(map[string]any)
	if assistantMsg["reasoning"] != "the user wants a quick factual answer" {
		t.Fatalf("reasoning = %#v, want the replayed reasoning_content value", assistantMsg["reasoning"])
	}
	if _, present := assistantMsg["reasoning_content"]; present {
		t.Fatal("reasoning_content should be renamed away, not duplicated alongside reasoning")
	}
	if req.Messages[1].ExtraFields.Lookup("reasoning") != nil {
		t.Fatal("ChatCompletion() mutated the caller's request")
	}
}

func TestChatCompletion_DoesNotOverrideExistingReasoningField(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vllm",
			"created":1,
			"model":"Qwen3.8-27B",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	var req core.ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"Qwen3.8-27B",
		"messages":[
			{"role":"assistant","content":"12-15 years","reasoning":"current field","reasoning_content":"stale legacy value"}
		]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.ChatCompletion(context.Background(), &req); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	messages, _ := gotBody["messages"].([]any)
	assistantMsg, _ := messages[0].(map[string]any)
	if assistantMsg["reasoning"] != "current field" {
		t.Fatalf("reasoning = %#v, want the untouched current-field value", assistantMsg["reasoning"])
	}
}

func TestAdaptChatRequest_NoOpWithoutLegacyReasoningContent(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{Role: "assistant", Content: "hi"}},
	}

	adapted, err := adaptChatRequest(req)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if adapted != req {
		t.Fatal("adaptChatRequest() copied a request it didn't need to change")
	}
}

func TestAdaptChatRequest_IgnoresNonAssistantMessages(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{Role: "tool", Content: "ok"}},
	}
	req.Messages[0].ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"reasoning_content": json.RawMessage(`"should not move"`),
	})

	adapted, err := adaptChatRequest(req)
	if err != nil {
		t.Fatalf("adaptChatRequest() error = %v", err)
	}
	if adapted != req {
		t.Fatal("adaptChatRequest() should not adapt non-assistant messages")
	}
}

func TestAdaptChatRequest_NilRequest(t *testing.T) {
	adapted, err := adaptChatRequest(nil)
	if err != nil {
		t.Fatalf("adaptChatRequest(nil) error = %v", err)
	}
	if adapted != nil {
		t.Fatal("adaptChatRequest(nil) should return nil")
	}
}
