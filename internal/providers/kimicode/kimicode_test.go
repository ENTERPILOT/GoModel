package kimicode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Kimi Code is a thin wrapper over the shared chat-centric adapter and
// forwards embeddings upstream unchanged, so the shared contract covers its
// surface. The Responses API is forwarded natively to the upstream /responses
// endpoint.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "kimicode",
		DefaultBaseURL: "https://api.kimi.com/coding/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		Embeddings:      true,
		NativeResponses: true,
	})
}

func boolPtr(b bool) *bool { return &b }

// newTestProvider builds a provider wired to the test server through the
// injected HTTP client, matching how the shared contract constructs it.
func newTestProvider(server *httptest.Server) core.Provider {
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	return New(providers.ProviderConfig{APIKey: "kimi-key", BaseURL: server.URL}, opts)
}

func TestAdaptResponsesRequest(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		assert.Nil(t, adaptResponsesRequest(nil))
	})

	t.Run("clean request is returned unchanged", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi"}
		assert.Same(t, req, adaptResponsesRequest(req))
	})

	t.Run("store true is pinned to false", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi", Store: boolPtr(true)}
		got := adaptResponsesRequest(req)
		require.NotSame(t, req, got, "adapted request should be a copy")
		require.NotNil(t, got.Store)
		assert.False(t, *got.Store)
		// The caller's request must not be mutated.
		assert.True(t, *req.Store, "original request Store was mutated")
	})

	t.Run("previous_response_id is preserved", func(t *testing.T) {
		// adaptResponsesRequest does not touch PreviousResponseID; the
		// Responses/StreamResponses methods reject it instead (tested below).
		req := &core.ResponsesRequest{
			Model:              "kimi-for-coding",
			Input:              "hi",
			PreviousResponseID: "resp_old",
			Store:              boolPtr(false),
		}
		got := adaptResponsesRequest(req)
		require.Equal(t, "resp_old", got.PreviousResponseID)
		require.NotNil(t, got.Store)
		assert.False(t, *got.Store, "explicit store=false should stay false")
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
		"input_tokens_details": {"cache_write_tokens": 12, "cached_tokens": 88},
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

	provider := newTestProvider(server)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: "Say OK",
		Store: boolPtr(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "/responses", gotPath)
	assert.Equal(t, "Bearer kimi-key", gotAuth)
	// store=true is pinned to false before the request leaves.
	assert.Equal(t, false, gotBody["store"], "wire store")
	assert.NotContains(t, gotBody, "stream", "non-streaming request must not set stream on the wire")

	assert.Equal(t, "resp_golden", resp.ID)
	assert.Equal(t, "kimi-for-coding", resp.Model)
	assert.Equal(t, int64(1788866014), resp.CompletedAt)
	require.NotNil(t, resp.Store)
	assert.False(t, *resp.Store)
	require.Len(t, resp.Output, 2)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 141, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 12, resp.Usage.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 88, resp.Usage.PromptTokensDetails.CachedTokens)
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

	provider := newTestProvider(server)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: "Say OK",
		Store: boolPtr(true),
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "/responses", gotPath)
	assert.Equal(t, true, gotBody["stream"], "wire stream")
	assert.Contains(t, string(body), "event: response.completed")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(string(body)), "data: [DONE]"),
		"stream should end with data: [DONE], got %q", string(body))
}

// Kimi Code retains no responses, so a request chaining from an earlier
// response must be rejected before any upstream call instead of being
// answered statelessly.
func TestResponses_RejectsPreviousResponseID(t *testing.T) {
	upstreamHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newTestProvider(server)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:              "kimi-for-coding",
		Input:              "Say OK",
		PreviousResponseID: "resp_old",
	})
	require.Error(t, err)
	assert.Nil(t, resp)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr, "error type = %T, want *core.GatewayError", err)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Error(), "previous_response_id")
	assert.Equal(t, 0, upstreamHits, "rejected request must not reach the upstream")
}

func TestStreamResponses_RejectsPreviousResponseID(t *testing.T) {
	upstreamHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newTestProvider(server)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:              "kimi-for-coding",
		Input:              "Say OK",
		PreviousResponseID: "resp_old",
	})
	require.Error(t, err)
	assert.Nil(t, stream)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr, "error type = %T, want *core.GatewayError", err)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	assert.Contains(t, gatewayErr.Error(), "previous_response_id")
	assert.Equal(t, 0, upstreamHits, "rejected request must not reach the upstream")
}

// SetBaseURL must retarget both the chat-centric adapter and the native
// Responses adapter.
func TestSetBaseURL(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesGoldenBody))
	}))
	defer server.Close()

	p := New(providers.ProviderConfig{APIKey: "kimi-key"}, providertest.Options(llmclient.Hooks{}))
	kp, ok := p.(*Provider)
	require.True(t, ok)
	require.Equal(t, defaultBaseURL, kp.GetBaseURL())

	kp.SetBaseURL(server.URL)

	assert.Equal(t, server.URL, kp.GetBaseURL())
	assert.Equal(t, server.URL, kp.responses.GetBaseURL())

	_, err := p.Responses(context.Background(), &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: "Say OK",
	})
	require.NoError(t, err)
	assert.Equal(t, "/responses", gotPath)
}
