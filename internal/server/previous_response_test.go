package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsestore"
)

// translatedResponsesProvider routes every model to a provider type without
// the native Responses lifecycle, the way Anthropic and Gemini are served.
type translatedResponsesProvider struct {
	*capturingProvider
	native []string
}

func (p *translatedResponsesProvider) NativeResponseProviderTypes() []string { return p.native }

func previousResponseTestProvider(t *testing.T, providerType string) *translatedResponsesProvider {
	t.Helper()
	inner := conversationTestProvider(t)
	inner.providerTypes = map[string]string{"gpt-5-mini": providerType}
	return &translatedResponsesProvider{capturingProvider: inner, native: []string{"openai"}}
}

func waitForStoredResponse(t *testing.T, store responsestore.Store, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.Get(context.Background(), id); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("response %s was not stored in time", id)
}

func forwardedInputItems(t *testing.T, provider *capturingProvider) []map[string]any {
	t.Helper()
	if provider.capturedResponsesReq == nil {
		t.Fatal("provider did not receive a responses request")
	}
	raw, ok := provider.capturedResponsesReq.Input.([]any)
	if !ok {
		t.Fatalf("forwarded input = %#v, want []any", provider.capturedResponsesReq.Input)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("forwarded item = %#v, want object", item)
		}
		items = append(items, m)
	}
	return items
}

func TestResponsesWithPreviousResponseID_ResolvesFromStoreForTranslatedProvider(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first responses status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, srv.handler.currentResponseStore(), "resp_conv_1")

	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_conv_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chained responses status = %d (%s)", rec.Code, rec.Body.String())
	}
	forwarded := provider.capturedResponsesReq
	if forwarded.PreviousResponseID != "" {
		t.Fatalf("previous_response_id must be stripped before dispatch, got %q", forwarded.PreviousResponseID)
	}
	items := forwardedInputItems(t, provider.capturingProvider)
	if len(items) != 3 {
		t.Fatalf("forwarded %d items, want previous input + previous output + new input: %#v", len(items), items)
	}
	if items[0]["role"] != "user" || items[1]["role"] != "assistant" || items[2]["role"] != "user" {
		t.Fatalf("forwarded roles = %v/%v/%v, want user/assistant/user", items[0]["role"], items[1]["role"], items[2]["role"])
	}
	if _, hasID := items[0]["id"]; hasID {
		t.Fatalf("stored item id must be stripped before dispatch, got %#v", items[0])
	}
	if text, _ := json.Marshal(items[1]["content"]); !strings.Contains(string(text), "the word is zebra") {
		t.Fatalf("previous output not replayed: %#v", items[1])
	}
}

// TestResponsesWithPreviousResponseID_ChainCarriesFullHistory covers a third
// turn chained on the second: the stored input items of a chained response
// already hold the history it was built from, so one hop replays all of it.
func TestResponsesWithPreviousResponseID_ChainCarriesFullHistory(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)
	store := srv.handler.currentResponseStore()

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn one"}`); rec.Code != http.StatusOK {
		t.Fatalf("turn one status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, store, "resp_conv_1")

	provider.responsesResponse.ID = "resp_conv_2"
	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn two","previous_response_id":"resp_conv_1"}`); rec.Code != http.StatusOK {
		t.Fatalf("turn two status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, store, "resp_conv_2")

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn three","previous_response_id":"resp_conv_2"}`); rec.Code != http.StatusOK {
		t.Fatalf("turn three status = %d (%s)", rec.Code, rec.Body.String())
	}
	items := forwardedInputItems(t, provider.capturingProvider)
	if len(items) != 5 {
		t.Fatalf("turn three forwarded %d items, want 5 (two full turns + new input): %#v", len(items), items)
	}
}

func TestResponsesWithPreviousResponseID_StreamingChainedTurn(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	provider.streamData = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n"
	srv := New(provider, nil)

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`); rec.Code != http.StatusOK {
		t.Fatalf("first responses status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, srv.handler.currentResponseStore(), "resp_conv_1")

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_conv_1","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chained streaming status = %d (%s)", rec.Code, rec.Body.String())
	}
	if provider.capturedResponsesReq.PreviousResponseID != "" {
		t.Fatal("previous_response_id must be stripped before a streaming dispatch")
	}
	if items := forwardedInputItems(t, provider.capturingProvider); len(items) != 3 {
		t.Fatalf("streaming chained turn forwarded %d items, want 3", len(items))
	}
}

func TestResponsesWithPreviousResponseID_NativeProviderKeepsForwarding(t *testing.T) {
	provider := previousResponseTestProvider(t, "openai")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","previous_response_id":"resp_native_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	forwarded := provider.capturedResponsesReq
	if forwarded == nil || forwarded.PreviousResponseID != "resp_native_1" {
		t.Fatalf("native provider must receive previous_response_id untouched, got %#v", forwarded)
	}
	if _, ok := forwarded.Input.(string); !ok {
		t.Fatalf("native provider input must be untouched, got %#v", forwarded.Input)
	}
}

func TestResponsesWithPreviousResponseID_UnknownIDReturns404(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","previous_response_id":"resp_missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Previous response with id 'resp_missing' not found.") {
		t.Fatalf("body = %s, want OpenAI-shaped not-found message", rec.Body.String())
	}
	if provider.capturedResponsesReq != nil {
		t.Fatal("a missing previous response must not reach the provider")
	}
}

// TestApplyResponsesPreviousResponse_ScopedTenantCannotChainAcrossScopes
// reports another tenant's stored response exactly like a missing one.
func TestApplyResponsesPreviousResponse_ScopedTenantCannotChainAcrossScopes(t *testing.T) {
	store := responsestore.NewMemoryStore()
	defer func() { _ = store.Close() }()
	if err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_t", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
		UserPath:   "/tenant-b",
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	s := &translatedInferenceService{provider: previousResponseTestProvider(t, "anthropic"), responseStore: store}
	workflow := &core.Workflow{ProviderType: "anthropic"}
	req := &core.ResponsesRequest{Model: "gpt-5-mini", Input: "again?", PreviousResponseID: "resp_t"}

	foreign := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-a"})
	if _, err := s.applyResponsesPreviousResponse(foreign, req, workflow); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("foreign scope error = %v, want not found", err)
	}

	owner := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-b"})
	patched, err := s.applyResponsesPreviousResponse(owner, req, workflow)
	if err != nil {
		t.Fatalf("owner scope: %v", err)
	}
	if patched.PreviousResponseID != "" {
		t.Fatalf("previous_response_id = %q, want stripped", patched.PreviousResponseID)
	}
	if items, ok := patched.Input.([]any); !ok || len(items) != 3 {
		t.Fatalf("patched input = %#v, want 3 items", patched.Input)
	}
}
