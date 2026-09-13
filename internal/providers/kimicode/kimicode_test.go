package kimicode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// kimibgeM3EmbedModel is the model ID used by these tests for the Kimi Code /embeddings endpoint.
//
// NOTE: "bge_m3_embed" is not part of Kimi Code's documented public model catalogue. It is
// retained here because the Kimi Code provider package currently forwards embedding model IDs
// unchanged through the OpenAI-compatible adapter. Because the model is undocumented upstream,
// its name, behaviour, or availability may change without notice; if Kimi Code rotates the ID
// these tests (and the provider's embedding round-trip) will need to be updated.
const kimibgeM3EmbedModel = "bge_m3_embed"

func boolPtr(b bool) *bool { return &b }

func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}

	concrete, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *kimicode.Provider", provider)
	}
	if concrete.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

func TestNewWithHTTPClient_ReturnsProvider(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}
	if provider.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

func TestRegistration_TypeIsKimicode(t *testing.T) {
	if Registration.Type != "kimicode" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "kimicode")
	}
	if Registration.New == nil {
		t.Error("Registration.New should not be nil")
	}
	if Registration.Discovery.DefaultBaseURL == "" {
		t.Error("Registration.Discovery.DefaultBaseURL should not be empty")
	}
}

func TestEmbeddings_RoundTrip(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"model": "bge_m3_embed",
			"data": [
				{"object": "embedding", "embedding": [0.1, 0.2, 0.3], "index": 0}
			],
			"usage": {"prompt_tokens": 3, "total_tokens": 3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: kimibgeM3EmbedModel,
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp == nil {
		t.Fatal("Embeddings() response should not be nil")
	}
	if resp.Model != kimibgeM3EmbedModel {
		t.Errorf("resp.Model = %q, want %q", resp.Model, kimibgeM3EmbedModel)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer kimi-key" {
		t.Errorf("authorization = %q, want %q", gotAuth, "Bearer kimi-key")
	}
}

func TestAdaptResponsesRequest(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		if got := adaptResponsesRequest(nil); got != nil {
			t.Errorf("adaptResponsesRequest(nil) = %v, want nil", got)
		}
	})

	t.Run("clean request is returned unchanged", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi"}
		if got := adaptResponsesRequest(req); got != req {
			t.Error("clean request should be returned by identity")
		}
	})

	t.Run("store true is pinned to false", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi", Store: boolPtr(true)}
		got := adaptResponsesRequest(req)
		if got == req {
			t.Fatal("adapted request should be a copy")
		}
		if got.Store == nil || *got.Store {
			t.Errorf("Store = %v, want false", got.Store)
		}
		// The caller's request must not be mutated.
		if req.Store == nil || !*req.Store {
			t.Error("original request Store was mutated")
		}
	})

	t.Run("previous_response_id is dropped", func(t *testing.T) {
		req := &core.ResponsesRequest{
			Model:              "kimi-for-coding",
			Input:              "hi",
			PreviousResponseID: "resp_old",
			Store:              boolPtr(false),
		}
		got := adaptResponsesRequest(req)
		if got.PreviousResponseID != "" {
			t.Errorf("PreviousResponseID = %q, want empty", got.PreviousResponseID)
		}
		if got.Store == nil || *got.Store {
			t.Errorf("explicit store=false should stay false, got %v", got.Store)
		}
	})
}

// responsesGoldenBody mirrors a real non-streaming /responses reply from the
// Kimi Code upstream (recorded 2026-09-08, trimmed to the members GoModel
// consumes). The upstream reply keeps extra members (prompt_cache_key,
// safety_identifier, service_tier); unknown members are ignored on decode.
const responsesGoldenBody = `{
	"id": "resp_golden",
	"object": "response",
	"created_at": 1788866012,
	"completed_at": 1788866014,
	"status": "completed",
	"output": [
		{
			"type": "reasoning",
			"id": "rs_golden",
			"status": "completed",
			"summary": [{"type": "summary_text", "text": "Simple request."}]
		},
		{
			"type": "message",
			"id": "msg_golden",
			"status": "completed",
			"role": "assistant",
			"content": [{"type": "output_text", "text": "OK", "annotations": []}]
		}
	],
	"usage": {
		"input_tokens": 88,
		"input_tokens_details": {"cache_write_tokens": 0, "cached_tokens": 88},
		"output_tokens": 53,
		"output_tokens_details": {"reasoning_tokens": 37},
		"total_tokens": 141
	},
	"store": false,
	"model": "kimi-for-coding"
}`

func TestResponses_NativeEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesGoldenBody))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:              "kimi-for-coding",
		Input:              "Say OK",
		Store:              boolPtr(true),
		PreviousResponseID: "resp_old",
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer kimi-key" {
		t.Errorf("authorization = %q, want %q", gotAuth, "Bearer kimi-key")
	}
	// Rejected state is adapted away before the request leaves.
	if store, ok := gotBody["store"].(bool); !ok || store {
		t.Errorf("wire store = %v, want false", gotBody["store"])
	}
	if _, present := gotBody["previous_response_id"]; present {
		t.Error("wire body still carries previous_response_id")
	}
	if gotBody["stream"] == true {
		t.Error("non-streaming request must not set stream on the wire")
	}

	if resp.ID != "resp_golden" {
		t.Errorf("resp.ID = %q, want %q", resp.ID, "resp_golden")
	}
	if resp.Model != "kimi-for-coding" {
		t.Errorf("resp.Model = %q, want %q", resp.Model, "kimi-for-coding")
	}
	if len(resp.Output) != 2 {
		t.Fatalf("len(resp.Output) = %d, want 2", len(resp.Output))
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 141 {
		t.Errorf("resp.Usage = %+v, want total_tokens 141", resp.Usage)
	}
}

func TestStreamResponses_NativeEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		// No trailing [DONE]: providers.EnsureResponsesDone must append it.
		_, _ = io.WriteString(w, strings.Join([]string{
			`event: response.created`,
			`data: {"type":"response.created","response":{"id":"resp_stream","object":"response","status":"in_progress","model":"kimi-for-coding"}}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","response":{"id":"resp_stream","object":"response","status":"completed","model":"kimi-for-coding"}}`,
			``,
		}, "\n"))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:              "kimi-for-coding",
		Input:              "Say OK",
		Store:              boolPtr(true),
		PreviousResponseID: "resp_old",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer func() { _ = stream.Close() }()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if streamOn, ok := gotBody["stream"].(bool); !ok || !streamOn {
		t.Errorf("wire stream = %v, want true", gotBody["stream"])
	}
	if _, present := gotBody["previous_response_id"]; present {
		t.Error("wire body still carries previous_response_id")
	}
	if !bytes.Contains(body, []byte("event: response.completed")) {
		t.Error("stream missing response.completed event")
	}
	if !bytes.HasSuffix(bytes.TrimSpace(body), []byte("data: [DONE]")) {
		t.Errorf("stream should end with data: [DONE], got %q", string(body))
	}
}
