package kimicode

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// newTestProvider builds a provider wired to the test server through the
// injected HTTP client, matching how the shared contract constructs it.
func newTestProvider(server *httptest.Server) core.Provider {
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	return New(providers.ProviderConfig{APIKey: "kimi-key", BaseURL: server.URL}, opts)
}

func TestRejectPreviousResponseID(t *testing.T) {
	t.Run("nil request passes through", func(t *testing.T) {
		assert.NoError(t, rejectPreviousResponseID(nil))
	})

	t.Run("clean request passes through", func(t *testing.T) {
		assert.NoError(t, rejectPreviousResponseID(&core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi"}))
	})
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
		req := &core.ResponsesRequest{Model: "kimi-for-coding", Input: "hi", Store: new(true)}
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
			Store:              new(false),
		}
		got := adaptResponsesRequest(req)
		assert.Equal(t, "resp_old", got.PreviousResponseID)
		require.NotNil(t, got.Store)
		assert.False(t, *got.Store, "explicit store=false should stay false")
	})

	t.Run("whitespace-only previous_response_id is cleared", func(t *testing.T) {
		req := &core.ResponsesRequest{
			Model:              "kimi-for-coding",
			Input:              "hi",
			PreviousResponseID: "   ",
		}
		got := adaptResponsesRequest(req)
		require.NotSame(t, req, got, "adapted request should be a copy")
		assert.Empty(t, got.PreviousResponseID)
		assert.Equal(t, "   ", req.PreviousResponseID, "original request was mutated")
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

// TestResponses_ForwardsGatewayReplayedHistory covers what the gateway
// dispatches after expanding a previous_response_id chain against its
// response store: the stored history is replayed into input as items
// (reasoning and message items among them, IDs stripped) and
// previous_response_id is cleared. Kimi Code must forward that replayed
// input to /responses as stored instead of rejecting it.
func TestResponses_ForwardsGatewayReplayedHistory(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesGoldenBody)

	provider := newTestProvider(server)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "remember: zebra"}},
			},
			map[string]any{
				"type":   "reasoning",
				"status": "completed",
				"summary": []any{
					map[string]any{"type": "summary_text", "text": "thinking about zebras"},
				},
			},
			map[string]any{
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "the word is zebra"}},
			},
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "what is the word?"}},
			},
		},
		Store: new(true),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	req := capture.Last(t)
	assert.Equal(t, "/responses", req.Path)
	wire := req.JSON(t)
	assert.Equal(t, false, wire["store"], "store is pinned to false on the wire")
	assert.NotContains(t, wire, "previous_response_id")

	items, ok := wire["input"].([]any)
	require.True(t, ok, "wire input = %#v, want replayed items", wire["input"])
	require.Len(t, items, 4)
	reasoning, ok := items[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "reasoning", reasoning["type"], "replayed reasoning item must be forwarded unchanged")
	assert.Equal(t, "user", items[3].(map[string]any)["role"], "the client's own turn is replayed last")
}

// Kimi Code retains no responses, so a request chaining from an earlier
// response that the gateway could not expand (no store configured) must be
// rejected before any upstream call instead of being answered statelessly.
func TestResponses_RejectsPreviousResponseID(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesGoldenBody)

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
	assert.Equal(t, 0, capture.Count(), "rejected request must not reach the upstream")

	t.Run("whitespace-only ID passes through", func(t *testing.T) {
		// The gateway and the chat-translation validator treat a
		// whitespace-only ID as empty; the provider must behave the same,
		// and the unresolvable value must not reach the wire (omitempty
		// does not omit a non-empty whitespace string).
		resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
			Model:              "kimi-for-coding",
			Input:              "Say OK",
			PreviousResponseID: "   ",
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, 1, capture.Count(), "whitespace-only ID is treated as empty")
		wire := capture.Last(t).JSON(t)
		_, present := wire["previous_response_id"]
		assert.False(t, present, "whitespace-only ID must be omitted from the wire")
	})

	t.Run("conversation reference is rejected", func(t *testing.T) {
		before := capture.Count()
		resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
			Model:        "kimi-for-coding",
			Input:        "Say OK",
			Conversation: &core.ResponsesConversationRef{ID: "conv_old"},
		})
		require.Error(t, err)
		assert.Nil(t, resp)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
		assert.Contains(t, gatewayErr.Error(), "conversation")
		assert.Equal(t, before, capture.Count(), "conversation request must not reach the upstream")
	})
}

func TestStreamResponses_RejectsPreviousResponseID(t *testing.T) {
	server, capture := providertest.SSEServer(t, "")

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
	assert.Equal(t, 0, capture.Count(), "rejected request must not reach the upstream")
}

// SetBaseURL must retarget the single adapter serving both the chat-centric
// surface and the native Responses endpoint.
func TestSetBaseURL(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesGoldenBody)

	p := New(providers.ProviderConfig{APIKey: "kimi-key"}, providertest.Options(llmclient.Hooks{}))
	kp, ok := p.(*Provider)
	require.True(t, ok)
	require.Equal(t, defaultBaseURL, kp.GetBaseURL())

	kp.SetBaseURL(server.URL)

	assert.Equal(t, server.URL, kp.GetBaseURL())

	_, err := p.Responses(context.Background(), &core.ResponsesRequest{
		Model: "kimi-for-coding",
		Input: "Say OK",
	})
	require.NoError(t, err)
	require.Equal(t, 1, capture.Count())
	assert.Equal(t, "/responses", capture.Last(t).Path)
}
