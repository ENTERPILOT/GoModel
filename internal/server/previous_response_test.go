package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

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
	if !strings.Contains(rec.Body.String(), `"previous_response_id":"resp_conv_1"`) {
		t.Fatalf("chained response must echo previous_response_id: %s", rec.Body.String())
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
	if text, _ := json.Marshal(items[2]["content"]); !strings.Contains(string(text), "turn two") {
		t.Fatalf("history is not oldest first: %#v", items)
	}

	// Each snapshot keeps only its own input and links to its predecessor.
	second, err := store.Get(context.Background(), "resp_conv_2")
	if err != nil {
		t.Fatalf("load turn two: %v", err)
	}
	if second.Response.PreviousResponseID != "resp_conv_1" || len(second.InputItems) != 1 {
		t.Fatalf("turn two snapshot previous=%q input items=%d, want resp_conv_1 and 1", second.Response.PreviousResponseID, len(second.InputItems))
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

// TestPatchResponsesAttempt_ScopedTenantCannotChainAcrossScopes
// reports another tenant's stored response exactly like a missing one.
func TestPatchResponsesAttempt_ScopedTenantCannotChainAcrossScopes(t *testing.T) {
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
	req := &core.ResponsesRequest{Model: "gpt-5-mini", Input: "again?", PreviousResponseID: "resp_t"}

	foreign := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-a"})
	if _, err := s.PatchResponsesAttempt(foreign, req, "anthropic"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("foreign scope error = %v, want not found", err)
	}

	owner := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-b"})
	patched, err := s.PatchResponsesAttempt(owner, req, "anthropic")
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

func streamedResponseData(id, text string) string {
	return streamedTerminalData("response.completed", "completed", id, text)
}

func streamedTerminalData(event, status, id, text string) string {
	return "event: " + event + "\ndata: {\"type\":\"" + event + "\",\"response\":{\"id\":\"" + id +
		"\",\"object\":\"response\",\"status\":\"" + status + "\",\"output\":[{\"id\":\"msg_" + id +
		"\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + text +
		"\"}]}]}}\n\ndata: [DONE]\n\n"
}

// TestResponsesWithPreviousResponseID_StreamedPredecessorChains covers the
// common agent loop: every turn streams, and each one chains on the last. A
// streamed response is snapshotted from its terminal event, so a later turn
// can chain on it and replays the whole streamed chain.
func TestResponsesWithPreviousResponseID_StreamedPredecessorChains(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	provider.streamData = streamedResponseData("resp_s1", "the word is zebra")
	srv := New(provider, nil)
	store := srv.handler.currentResponseStore()

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra","stream":true}`); rec.Code != http.StatusOK {
		t.Fatalf("turn one status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, store, "resp_s1")
	stored, err := store.Get(context.Background(), "resp_s1")
	if err != nil {
		t.Fatalf("get resp_s1: %v", err)
	}
	if len(stored.InputItems) != 1 || len(stored.Response.Output) != 1 {
		t.Fatalf("streamed snapshot holds %d input items and %d output items, want 1 and 1", len(stored.InputItems), len(stored.Response.Output))
	}

	provider.streamData = streamedResponseData("resp_s2", "still zebra")
	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"sure?","previous_response_id":"resp_s1","stream":true}`); rec.Code != http.StatusOK {
		t.Fatalf("turn two status = %d (%s)", rec.Code, rec.Body.String())
	}
	waitForStoredResponse(t, store, "resp_s2")
	stored, err = store.Get(context.Background(), "resp_s2")
	if err != nil {
		t.Fatalf("get resp_s2: %v", err)
	}
	if stored.Response.PreviousResponseID != "resp_s1" {
		t.Fatalf("streamed chained snapshot links to %q, want resp_s1", stored.Response.PreviousResponseID)
	}

	provider.capturedResponsesReq = nil
	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_s2"}`); rec.Code != http.StatusOK {
		t.Fatalf("turn three status = %d (%s)", rec.Code, rec.Body.String())
	}
	items := forwardedInputItems(t, provider.capturingProvider)
	if len(items) != 5 {
		t.Fatalf("forwarded %d items, want two streamed turns plus the new input: %#v", len(items), items)
	}
	if items[1]["role"] != "assistant" || items[3]["role"] != "assistant" {
		t.Fatalf("forwarded roles = %v/%v, want assistant at 1 and 3", items[1]["role"], items[3]["role"])
	}
	if text, _ := json.Marshal(items[1]["content"]); !strings.Contains(string(text), "the word is zebra") {
		t.Fatalf("streamed output not replayed: %#v", items[1])
	}
}

// TestResponsesWithPreviousResponseID_StreamedTerminalEventsAreStored covers
// the other terminal events: a truncated or failed streamed turn is stored
// like its buffered counterpart, so a client can retrieve it and chain on it.
func TestResponsesWithPreviousResponseID_StreamedTerminalEventsAreStored(t *testing.T) {
	for _, tc := range []struct{ event, status string }{
		{"response.incomplete", "incomplete"},
		{"response.failed", "failed"},
	} {
		t.Run(tc.event, func(t *testing.T) {
			provider := previousResponseTestProvider(t, "anthropic")
			provider.streamData = streamedTerminalData(tc.event, tc.status, "resp_t", "partial")
			srv := New(provider, nil)
			store := srv.handler.currentResponseStore()

			if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"go","stream":true}`); rec.Code != http.StatusOK {
				t.Fatalf("streaming status = %d (%s)", rec.Code, rec.Body.String())
			}
			waitForStoredResponse(t, store, "resp_t")
			stored, err := store.Get(context.Background(), "resp_t")
			if err != nil {
				t.Fatalf("get resp_t: %v", err)
			}
			if stored.Response.Status != tc.status {
				t.Fatalf("stored status = %q, want %q", stored.Response.Status, tc.status)
			}

			provider.capturedResponsesReq = nil
			if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"continue","previous_response_id":"resp_t"}`); rec.Code != http.StatusOK {
				t.Fatalf("chained status = %d (%s)", rec.Code, rec.Body.String())
			}
			if items := forwardedInputItems(t, provider.capturingProvider); len(items) != 3 {
				t.Fatalf("forwarded %d items, want 3", len(items))
			}
		})
	}
}

func TestResponsesWithPreviousResponseID_StreamedWithStoreFalseIsNotStored(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	provider.streamData = streamedResponseData("resp_s", "not kept")
	srv := New(provider, nil)

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"streamed","stream":true,"store":false}`); rec.Code != http.StatusOK {
		t.Fatalf("streaming status = %d (%s)", rec.Code, rec.Body.String())
	}
	provider.capturedResponsesReq = nil

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_s"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404: store:false responses are not stored", rec.Code, rec.Body.String())
	}
	if provider.capturedResponsesReq != nil {
		t.Fatal("a chained turn on an unstored id must not reach the provider")
	}
}

// heldSnapshotStore holds every Create until released, standing in for a
// slow snapshot write.
type heldSnapshotStore struct {
	responsestore.Store
	release chan struct{}
}

func (s *heldSnapshotStore) Create(ctx context.Context, response *responsestore.StoredResponse) error {
	<-s.release
	return s.Store.Create(ctx, response)
}

// TestResponsesWithPreviousResponseID_WaitsForPendingSnapshot chains on a
// response whose snapshot write has not landed yet: the chained turn waits
// for it instead of reporting the response missing.
func TestResponsesWithPreviousResponseID_WaitsForPendingSnapshot(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)
	store := &heldSnapshotStore{Store: responsestore.NewMemoryStore(), release: make(chan struct{})}
	srv.handler.SetResponseStore(store)

	if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`); rec.Code != http.StatusOK {
		t.Fatalf("first responses status = %d (%s)", rec.Code, rec.Body.String())
	}

	type outcome struct {
		code int
		body string
	}
	result := make(chan outcome, 1)
	go func() {
		rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_conv_1"}`)
		result <- outcome{rec.Code, rec.Body.String()}
	}()
	select {
	case got := <-result:
		t.Fatalf("chained turn finished before the snapshot was written: %d %s", got.code, got.body)
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	got := <-result
	if got.code != http.StatusOK {
		t.Fatalf("chained status = %d (%s), want 200 after the snapshot landed", got.code, got.body)
	}
	if items := forwardedInputItems(t, provider.capturingProvider); len(items) != 3 {
		t.Fatalf("chained turn forwarded %d items, want 3", len(items))
	}
}

// chainingFailoverProvider records the request each attempt received and
// reports which provider types resolve previous_response_id natively.
type chainingFailoverProvider struct {
	*failoverProvider
	native   []string
	requests map[string]*core.ResponsesRequest
}

func (p *chainingFailoverProvider) NativeResponseProviderTypes() []string { return p.native }

func (p *chainingFailoverProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	p.requests[requestSelector(req.Model, req.Provider)] = req
	return p.failoverProvider.Responses(ctx, req)
}

func (p *chainingFailoverProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	p.requests[requestSelector(req.Model, req.Provider)] = req
	return p.failoverProvider.StreamResponses(ctx, req)
}

// newChainingFailoverHandler builds a handler whose native primary fails with
// 503 and whose chat-translated failover target succeeds, with one stored
// response to chain on.
func newChainingFailoverHandler(t *testing.T) (*Handler, *chainingFailoverProvider) {
	t.Helper()
	provider := &chainingFailoverProvider{
		failoverProvider: &failoverProvider{
			responsesErrors: map[string]error{
				"gpt-5-mini": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
			},
			responsesResponses: map[string]*core.ResponsesResponse{
				"anthropic/claude": {ID: "resp_failover", Object: "response", Status: "completed", Output: []core.ResponsesOutputItem{}},
			},
			responsesStreams: map[string]string{
				"anthropic/claude": "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_failover\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n",
			},
			supportedModels: map[string]string{"gpt-5-mini": "openai", "anthropic/claude": "anthropic"},
		},
		native:   []string{"openai"},
		requests: map[string]*core.ResponsesRequest{},
	}
	handler := newHandler(provider, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "anthropic", Model: "claude"}},
	}, nil)
	store := responsestore.NewMemoryStore()
	handler.SetResponseStore(store)
	if err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_native", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	return handler, provider
}

// assertResolvedPerAttempt checks that the native primary received the id
// untouched while the translated failover received the replayed history.
func assertResolvedPerAttempt(t *testing.T, provider *chainingFailoverProvider) {
	t.Helper()
	primary := provider.requests["gpt-5-mini"]
	if primary == nil || primary.PreviousResponseID != "resp_native" {
		t.Fatalf("native primary request = %#v, want previous_response_id kept", primary)
	}
	if _, ok := primary.Input.(string); !ok {
		t.Fatalf("native primary input must be untouched, got %#v", primary.Input)
	}
	fallback := provider.requests["anthropic/claude"]
	if fallback == nil || fallback.PreviousResponseID != "" {
		t.Fatalf("translated failover request = %#v, want previous_response_id resolved and stripped", fallback)
	}
	items, ok := fallback.Input.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("translated failover input = %#v, want replayed history + new input (3 items)", fallback.Input)
	}
}

// TestResponsesWithPreviousResponseID_ResolvedPerFailoverAttempt keeps the id
// on the native primary's request and hands the chat-translated failover
// target the replayed history instead, so a failed native attempt does not
// leave the fallback with an id it cannot use and a healthy native primary
// never sees its request rewritten. Both dispatch paths go through the same
// per-attempt hook.
func TestResponsesWithPreviousResponseID_ResolvedPerFailoverAttempt(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "streaming"}[stream], func(t *testing.T) {
			handler, provider := newChainingFailoverHandler(t)
			body := `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_native"}`
			if stream {
				body = `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_native","stream":true}`
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			if err := handler.Responses(echo.New().NewContext(req, rec)); err != nil {
				t.Fatalf("handler.Responses() error = %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
			}
			if stream && !strings.Contains(rec.Body.String(), "response.completed") {
				t.Fatalf("streaming fallback body = %s, want the fallback stream", rec.Body.String())
			}
			assertResolvedPerAttempt(t, provider)
		})
	}
}

func TestPatchResponsesAttempt_SkipsEmptyReplayText(t *testing.T) {
	store := responsestore.NewMemoryStore()
	defer func() { _ = store.Close() }()
	if err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_e", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{
				{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: ""}}},
				{ID: "fc_1", Type: "function_call", CallID: "call_1", Name: "lookup", Arguments: "{}"},
			},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"look it up"}]}`)},
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	s := &translatedInferenceService{provider: previousResponseTestProvider(t, "anthropic"), responseStore: store}
	patched, err := s.PatchResponsesAttempt(context.Background(), &core.ResponsesRequest{Model: "gpt-5-mini", Input: "and?", PreviousResponseID: "resp_e"}, "anthropic")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	items, _ := patched.Input.([]any)
	if len(items) != 3 {
		t.Fatalf("patched input has %d items, want input + function_call + new input (empty message dropped): %#v", len(items), patched.Input)
	}
	if call, _ := items[1].(map[string]any); call["type"] != "function_call" {
		t.Fatalf("second item = %#v, want the function_call kept", items[1])
	}
}

// TestPatchResponsesAttempt_PendingSnapshotWaitFollowsAccessScope
// lets a caller wait on an in-flight write only when its access scope covers
// the write's user path: a foreign tenant learns nothing from the wait, while
// a globally scoped caller keeps its race protection.
func TestPatchResponsesAttempt_PendingSnapshotWaitFollowsAccessScope(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		wantWait bool
	}{
		{name: "foreign tenant does not wait", ctx: core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-a"}), wantWait: false},
		{name: "global scope waits", ctx: context.Background(), wantWait: true},
		{name: "owning tenant waits", ctx: core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-b"}), wantWait: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &translatedInferenceService{provider: previousResponseTestProvider(t, "anthropic"), responseStore: responsestore.NewMemoryStore()}
			done := s.trackPendingSnapshot("resp_t", "/tenant-b")

			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, _ = s.PatchResponsesAttempt(tt.ctx, &core.ResponsesRequest{Model: "gpt-5-mini", Input: "x", PreviousResponseID: "resp_t"}, "anthropic")
			}()
			select {
			case <-finished:
				if tt.wantWait {
					t.Fatal("request did not wait for the pending snapshot")
				}
			case <-time.After(time.Second):
				if !tt.wantWait {
					t.Fatal("request waited on a pending snapshot outside its access scope")
				}
			}
			s.finishPendingSnapshot("resp_t", done)
			<-finished
		})
	}
}
