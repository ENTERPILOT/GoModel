package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	require.Eventually(t, func() bool {
		_, err := store.Get(context.Background(), id)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "response %s was not stored in time", id)
}

func forwardedInputItems(t *testing.T, provider *capturingProvider) []map[string]any {
	t.Helper()
	require.NotNil(t, provider.capturedResponsesReq)

	raw, ok := provider.capturedResponsesReq.Input.([]any)
	require.True(t, ok, "forwarded input = %#v, want []any", provider.capturedResponsesReq.Input)

	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		require.True(t, ok, "forwarded item = %#v, want object", item)

		items = append(items, m)
	}
	return items
}

func TestResponsesWithPreviousResponseID_ResolvesFromStoreForTranslatedProvider(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	waitForStoredResponse(t, srv.handler.currentResponseStore(), "resp_conv_1")

	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_conv_1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"previous_response_id":"resp_conv_1"`)

	forwarded := provider.capturedResponsesReq
	require.Empty(t, forwarded.PreviousResponseID)

	items := forwardedInputItems(t, provider.capturingProvider)
	require.Len(t, items, 3)
	require.Equal(t, "user", items[0]["role"])
	require.Equal(t, "assistant", items[1]["role"])
	require.Equal(t, "user", items[2]["role"])
	_, hasID := items[0]["id"]
	require.False(t, hasID, "stored item id must be stripped before dispatch, got %#v", items[0])
	text, _ := json.Marshal(items[1]["content"])
	require.Contains(t, string(text), "the word is zebra", "previous output not replayed: %#v", items[1])
}

// TestResponsesWithPreviousResponseID_ChainCarriesFullHistory covers a third
// turn chained on the second: the stored input items of a chained response
// already hold the history it was built from, so one hop replays all of it.
func TestResponsesWithPreviousResponseID_ChainCarriesFullHistory(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)
	store := srv.handler.currentResponseStore()
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn one"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	waitForStoredResponse(t, store, "resp_conv_1")

	provider.responsesResponse.ID = "resp_conv_2"
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn two","previous_response_id":"resp_conv_1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	waitForStoredResponse(t, store, "resp_conv_2")
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"turn three","previous_response_id":"resp_conv_2"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	items := forwardedInputItems(t, provider.capturingProvider)
	require.Len(t, items, 5)
	text, _ := json.Marshal(items[2]["content"])
	require.Contains(t, string(text), "turn two", "history is not oldest first: %#v", items)

	// Each snapshot keeps only its own input and links to its predecessor.
	second, err := store.Get(context.Background(), "resp_conv_2")
	require.NoError(t, err)
	require.Equal(t, "resp_conv_1", second.Response.PreviousResponseID)
	require.Len(t, second.InputItems, 1)
}

func TestResponsesWithPreviousResponseID_StreamingChainedTurn(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	provider.streamData = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n"
	srv := New(provider, nil)
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	waitForStoredResponse(t, srv.handler.currentResponseStore(), "resp_conv_1")

	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_conv_1","stream":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, provider.capturedResponsesReq.PreviousResponseID)
	items := forwardedInputItems(t, provider.capturingProvider)
	require.Len(t, items, 3)
}

func TestResponsesWithPreviousResponseID_NativeProviderKeepsForwarding(t *testing.T) {
	provider := previousResponseTestProvider(t, "openai")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","previous_response_id":"resp_native_1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	forwarded := provider.capturedResponsesReq
	require.NotNil(t, forwarded)
	require.Equal(t, "resp_native_1", forwarded.PreviousResponseID)
	_, ok := forwarded.Input.(string)
	require.True(t, ok, "native provider input must be untouched, got %#v", forwarded.Input)
}

// storeChainedResponse records a response the gateway served for providerType,
// the way a finished turn is snapshotted.
func storeChainedResponse(t *testing.T, store responsestore.Store, id, providerType, userPath string) {
	t.Helper()
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: id, Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "the word is zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember: zebra"}]}`)},
		Provider:   providerType,
		UserPath:   userPath,
	})
	require.NoError(t, err, "store %s: %v", id, err)
}

// A response the gateway minted for a chat-translated provider exists only in
// the gateway's store, so a native provider rejects the id ("Expected an ID
// that begins with 'resp'"). The gateway holds the whole turn and replays it
// instead. An id it does not know is still forwarded, so native chains that
// started outside the gateway keep working.
func TestResponsesWithPreviousResponseID_NativeProviderReplaysGatewayMintedChain(t *testing.T) {
	tests := []struct {
		name         string
		providerType string
		wantItems    int
		wantID       string
	}{
		{name: "translated predecessor is replayed", providerType: "anthropic", wantItems: 3},
		{name: "native predecessor keeps the id", providerType: "openai", wantID: "resp_stored"},
		{name: "unknown provider keeps the id", providerType: "", wantID: "resp_stored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := previousResponseTestProvider(t, "openai")
			srv := New(provider, nil)
			storeChainedResponse(t, srv.handler.currentResponseStore(), "resp_stored", tt.providerType, "")

			rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_stored"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			forwarded := provider.capturedResponsesReq
			require.Equal(t, tt.wantID, forwarded.PreviousResponseID)

			if tt.wantItems == 0 {
				_, ok := forwarded.Input.(string)
				require.True(t, ok, "native input must be untouched, got %#v", forwarded.Input)

				return
			}
			items := forwardedInputItems(t, provider.capturingProvider)
			require.Len(t, items, tt.wantItems)
			text, _ := json.Marshal(items[1]["content"])
			require.Contains(t, string(text), "the word is zebra", "stored output not replayed: %#v", items[1])
			require.Contains(t, rec.Body.String(), `"previous_response_id":"resp_stored"`)
		})
	}
}

// A native provider resolves previous_response_id upstream under the
// gateway's shared credential, so forwarding another tenant's id would let
// one tenant continue - and read back - a conversation the same gateway hides
// from it on GET. Such an id is reported missing instead; an id the gateway
// does not hold still reaches the provider, so chains created against it
// directly keep working.
func TestResponsesWithPreviousResponseID_NativeProviderEnforcesOwnership(t *testing.T) {
	newService := func(t *testing.T) *translatedInferenceService {
		t.Helper()
		s := &translatedInferenceService{provider: previousResponseTestProvider(t, "openai"), responseStore: responsestore.NewMemoryStore()}
		storeChainedResponse(t, s.responseStore, "resp_owned", "openai", "/tenant-b")
		storeChainedResponse(t, s.responseStore, "resp_translated", "anthropic", "/tenant-b")
		return s
	}
	scoped := func(path string) context.Context {
		return core.WithAccessScope(context.Background(), core.AccessScope{UserPath: path})
	}

	tests := []struct {
		name       string
		ctx        context.Context
		id         string
		wantReplay bool
		wantErr    bool
	}{
		{name: "foreign tenant is refused", ctx: scoped("/tenant-a"), id: "resp_owned", wantErr: true},
		{name: "foreign tenant is refused for a gateway-minted id", ctx: scoped("/tenant-a"), id: "resp_translated", wantErr: true},
		{name: "owner forwards its native id", ctx: scoped("/tenant-b"), id: "resp_owned"},
		{name: "owner replays its gateway-minted id", ctx: scoped("/tenant-b"), id: "resp_translated", wantReplay: true},
		{name: "unknown id is forwarded", ctx: scoped("/tenant-a"), id: "resp_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newService(t)
			replay, err := s.nativePreviousResponse(tt.ctx, tt.id)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "not found")

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantReplay, replay)

			// Every dispatch attempt re-checks, so a native attempt of any
			// request is covered even when the history was not expanded.
			req := &core.ResponsesRequest{Model: "gpt-5-mini", Input: "again?", PreviousResponseID: tt.id}
			_, err = s.PatchResponsesAttempt(tt.ctx, req, "openai")
			require.NoError(t, err)
		})
	}
}

// The foreign-tenant rejection reaches the client as a 404, streamed or not,
// the way an unknown id does on a chat-translated provider.
func TestResponsesWithPreviousResponseID_ForeignTenantChainReturns404(t *testing.T) {
	for _, stream := range []bool{false, true} {
		provider := previousResponseTestProvider(t, "openai")
		srv := New(provider, nil)
		storeChainedResponse(t, srv.handler.currentResponseStore(), "resp_owned", "openai", "/tenant-b")

		body := `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_owned","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
		c, rec := echotest.Post(t, "/v1/responses", body)
		c.SetRequest(c.Request().WithContext(core.WithAccessScope(c.Request().Context(), core.AccessScope{UserPath: "/tenant-a"})))
		err := srv.handler.Responses(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, rec.Code, "stream=%v status = %d (%s), want 404", stream, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "Previous response with id 'resp_owned' not found.", "stream=%v body = %s, want the not-found message", stream, rec.Body.String())
		require.Nil(t, provider.capturedResponsesReq, "stream=%v another tenant's chain must not reach the provider", stream)
	}
}

func TestResponsesWithPreviousResponseID_UnknownIDReturns404(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	srv := New(provider, nil)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"hello","previous_response_id":"resp_missing"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Previous response with id 'resp_missing' not found.")
	require.Nil(t, provider.capturedResponsesReq)
}

// TestPatchResponsesAttempt_ScopedTenantCannotChainAcrossScopes
// reports another tenant's stored response exactly like a missing one.
func TestPatchResponsesAttempt_ScopedTenantCannotChainAcrossScopes(t *testing.T) {
	store := responsestore.NewMemoryStore()
	defer func() { _ = store.Close() }()
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_t", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
		UserPath:   "/tenant-b",
	})
	require.NoError(t, err)

	s := &translatedInferenceService{provider: previousResponseTestProvider(t, "anthropic"), responseStore: store}
	req := &core.ResponsesRequest{Model: "gpt-5-mini", Input: "again?", PreviousResponseID: "resp_t"}

	foreign := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-a"})
	_, err = s.PatchResponsesAttempt(foreign, req, "anthropic")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	owner := core.WithAccessScope(context.Background(), core.AccessScope{UserPath: "/tenant-b"})
	patched, err := s.PatchResponsesAttempt(owner, req, "anthropic")
	require.NoError(t, err)
	require.Empty(t, patched.PreviousResponseID)
	items, ok := patched.Input.([]any)
	require.True(t, ok)
	require.Len(t, items, 3)
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

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra","stream":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "previous_response_id", "an unchained stream must not name a predecessor: %s", rec.Body.String())

	waitForStoredResponse(t, store, "resp_s1")
	stored, err := store.Get(context.Background(), "resp_s1")
	require.NoError(t, err)
	require.Len(t, stored.InputItems, 1)
	require.Len(t, stored.Response.Output, 1)

	provider.streamData = streamedResponseData("resp_s2", "still zebra")
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"sure?","previous_response_id":"resp_s1","stream":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The client sees the link on the streamed response, as on a buffered one.
	require.Contains(t, rec.Body.String(), `"previous_response_id":"resp_s1"`)

	waitForStoredResponse(t, store, "resp_s2")
	stored, err = store.Get(context.Background(), "resp_s2")
	require.NoError(t, err)
	require.Equal(t, "resp_s1", stored.Response.PreviousResponseID)

	provider.capturedResponsesReq = nil
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is the word?","previous_response_id":"resp_s2"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	items := forwardedInputItems(t, provider.capturingProvider)
	require.Len(t, items, 5)
	require.Equal(t, "assistant", items[1]["role"])
	require.Equal(t, "assistant", items[3]["role"])
	text, _ := json.Marshal(items[1]["content"])
	require.Contains(t, string(text), "the word is zebra", "streamed output not replayed: %#v", items[1])
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
			rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"go","stream":true}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			waitForStoredResponse(t, store, "resp_t")
			stored, err := store.Get(context.Background(), "resp_t")
			require.NoError(t, err)
			require.Equal(t, tc.status, stored.Response.Status)

			provider.capturedResponsesReq = nil
			rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"continue","previous_response_id":"resp_t"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			items := forwardedInputItems(t, provider.capturingProvider)
			require.Len(t, items, 3)
		})
	}
}

func TestResponsesWithPreviousResponseID_StreamedWithStoreFalseIsNotStored(t *testing.T) {
	provider := previousResponseTestProvider(t, "anthropic")
	provider.streamData = streamedResponseData("resp_s", "not kept")
	srv := New(provider, nil)
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"streamed","stream":true,"store":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	provider.capturedResponsesReq = nil

	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_s"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Nil(t, provider.capturedResponsesReq)
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
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"remember: zebra"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

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
	require.Equal(t, http.StatusOK, got.code, "chained status = %d (%s), want 200 after the snapshot landed", got.code, got.body)
	items := forwardedInputItems(t, provider.capturingProvider)
	require.Len(t, items, 3)
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
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_native", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
	})
	require.NoError(t, err)

	return handler, provider
}

// assertResolvedPerAttempt checks that the native primary received the id
// untouched while the translated failover received the replayed history.
func assertResolvedPerAttempt(t *testing.T, provider *chainingFailoverProvider) {
	t.Helper()
	primary := provider.requests["gpt-5-mini"]
	require.NotNil(t, primary)
	require.Equal(t, "resp_native", primary.PreviousResponseID)
	_, ok := primary.Input.(string)
	require.True(t, ok, "native primary input must be untouched, got %#v", primary.Input)

	fallback := provider.requests["anthropic/claude"]
	require.NotNil(t, fallback)
	require.Empty(t, fallback.PreviousResponseID)

	items, ok := fallback.Input.([]any)
	require.True(t, ok)
	require.Len(t, items, 3)
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
			c, rec := echotest.Post(t, "/v1/responses", body)
			err := handler.Responses(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			if stream {
				require.Contains(t, rec.Body.String(), "response.completed", "want the fallback stream")
			}
			assertResolvedPerAttempt(t, provider)
		})
	}
}

func TestPatchResponsesAttempt_SkipsEmptyReplayText(t *testing.T) {
	store := responsestore.NewMemoryStore()
	defer func() { _ = store.Close() }()
	err := store.Create(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_e", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{
				{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: ""}}},
				{ID: "fc_1", Type: "function_call", CallID: "call_1", Name: "lookup", Arguments: "{}"},
			},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"look it up"}]}`)},
	})
	require.NoError(t, err)

	s := &translatedInferenceService{provider: previousResponseTestProvider(t, "anthropic"), responseStore: store}
	patched, err := s.PatchResponsesAttempt(context.Background(), &core.ResponsesRequest{Model: "gpt-5-mini", Input: "and?", PreviousResponseID: "resp_e"}, "anthropic")
	require.NoError(t, err)

	items, _ := patched.Input.([]any)
	require.Len(t, items, 3)
	call, _ := items[1].(map[string]any)
	require.Equal(t, "function_call", call["type"], "second item = %#v, want the function_call kept", items[1])
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
				require.False(t, tt.wantWait)

			case <-time.After(time.Second):
				require.True(t, tt.wantWait)
			}
			s.finishPendingSnapshot("resp_t", done)
			<-finished
		})
	}
}
