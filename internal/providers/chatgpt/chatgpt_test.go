package chatgpt

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// codexSSE is a minimal Codex-backend stream: one text delta and the terminal
// response.completed envelope.
const codexSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-terra"}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-terra","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}` + "\n\n" +
	"data: [DONE]\n\n"

// tokenWithAccount builds an unsigned JWT carrying the ChatGPT account claim.
func tokenWithAccount(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		accountIDClaim: map[string]string{"chatgpt_account_id": accountID},
		"exp":          1787235658,
	})
	require.NoError(t, err)

	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestRegistration_TypeIsChatGPT(t *testing.T) {
	assert.Equal(t, "chatgpt", Registration.Type)
	assert.NotNil(t, Registration.New)
	assert.Equal(t, defaultBaseURL, Registration.Discovery.DefaultBaseURL)
}

// TestStreamResponses_SendsCodexDialect locks the wire contract: the ChatGPT
// Codex backend requires stream/store pinned, rejects public Responses
// parameters it does not implement, and needs a list-shaped input.
func TestStreamResponses_SendsCodexDialect(t *testing.T) {
	srv, capture := providertest.SSEServer(t, codexSSE)
	token := tokenWithAccount(t, "acct-123")
	provider := newTestProvider(token, srv.URL, srv.Client(), llmclient.Hooks{})

	temperature := 0.7
	maxTokens := 128
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:                "gpt-5.6-terra",
		Input:                "Reply with exactly ok",
		Instructions:         "You are Codex.",
		Temperature:          &temperature,
		MaxOutputTokens:      &maxTokens,
		PreviousResponseID:   "resp_prev",
		Truncation:           "auto",
		User:                 "someone",
		PromptCacheRetention: "24h",
		Metadata:             map[string]string{"a": "b"},
		Include:              []string{"reasoning.encrypted_content"},
		Reasoning:            &core.Reasoning{Effort: "low"},
	})
	require.NoError(t, err)

	defer func() { _ = stream.Close() }()
	_, err = io.ReadAll(stream)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/responses", sent.Path)
	assert.Equal(t, "Bearer "+token, sent.Header.Get("Authorization"))
	assert.Equal(t, "acct-123", sent.Header.Get("chatgpt-account-id"))

	gotBody := sent.JSON(t)
	streamed, _ := gotBody["stream"].(bool)
	assert.True(t, streamed)
	require.Contains(t, gotBody, "store")
	stored, _ := gotBody["store"].(bool)
	assert.False(t, stored)
	assert.Equal(t, "You are Codex.", gotBody["instructions"])

	for _, field := range []string{"temperature", "max_output_tokens", "previous_response_id", "truncation", "user", "metadata", "top_p", "service_tier", "prompt_cache_retention"} {
		assert.NotContains(t, gotBody, field, "%s must not be sent to the Codex backend", field)
	}
	input, ok := gotBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	msg, _ := input[0].(map[string]any)
	assert.Equal(t, "user", msg["role"])
	assert.Equal(t, "message", msg["type"])

	// The Responses API spells input content "input_text"; core.ContentPart
	// would have rewritten it to the Chat Completions "text".
	parts, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)

	part, _ := parts[0].(map[string]any)
	assert.Equal(t, "input_text", part["type"])
	assert.Equal(t, "Reply with exactly ok", part["text"])
}

// TestStreamResponses_ForwardsPromptCacheAffinity covers the cache signals the
// Codex CLI sends: prompt_cache_key in the body and the session-id header.
// The backend routes its cache on the header, so a client key without a
// detected session must reach the header too.
func TestStreamResponses_ForwardsPromptCacheAffinity(t *testing.T) {
	longSession := strings.Repeat("s", maxPromptCacheKeyLength+1)
	tests := []struct {
		name       string
		session    string
		extras     map[string]json.RawMessage
		wantKey    string
		wantHeader string
	}{
		{
			name:       "client key wins over the session",
			session:    "sess-1",
			extras:     map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"pck_client"`)},
			wantKey:    "pck_client",
			wantHeader: "sess-1",
		},
		{
			name:       "session stands in for a missing key",
			session:    "sess-1",
			wantKey:    "sess-1",
			wantHeader: "sess-1",
		},
		{
			name:       "null key counts as missing",
			session:    "sess-1",
			extras:     map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`null`)},
			wantKey:    "sess-1",
			wantHeader: "sess-1",
		},
		{
			name:       "session too long for a key is sent only as a header",
			session:    longSession,
			wantHeader: longSession,
		},
		{
			name:       "client key without a session is also the header",
			extras:     map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"pck_client"`)},
			wantKey:    "pck_client",
			wantHeader: "pck_client",
		},
		{
			name:    "client key with a line break stays out of the header",
			extras:  map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"pck\r\nX-Injected: 1"`)},
			wantKey: "pck\r\nX-Injected: 1",
		},
		{
			name: "no key and no session sends neither",
		},
		{
			name: "unlisted extras stay dropped",
			extras: map[string]json.RawMessage{
				"prompt_cache_key":        json.RawMessage(`"pck_client"`),
				"prompt_cache_options":    json.RawMessage(`{"mode":"explicit"}`),
				"some_future_param":       json.RawMessage(`true`),
				"prompt_cache_breakpoint": json.RawMessage(`{"mode":"explicit"}`),
			},
			wantKey:    "pck_client",
			wantHeader: "pck_client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, capture := providertest.SSEServer(t, codexSSE)
			provider := newTestProvider(tokenWithAccount(t, "acct-123"), srv.URL, srv.Client(), llmclient.Hooks{})

			ctx := context.Background()
			if tt.session != "" {
				ctx = core.WithSessionID(ctx, tt.session)
			}
			stream, err := provider.StreamResponses(ctx, &core.ResponsesRequest{
				Model:       "gpt-5.6-terra",
				Input:       "hi",
				ExtraFields: core.UnknownJSONFieldsFromMap(tt.extras),
			})
			require.NoError(t, err)
			defer func() { _ = stream.Close() }()
			_, err = io.ReadAll(stream)
			require.NoError(t, err)

			sent := capture.Last(t)
			assert.Equal(t, tt.wantHeader, sent.Header.Get(sessionHeader))
			body := sent.JSON(t)
			if tt.wantKey == "" {
				assert.NotContains(t, body, "prompt_cache_key")
			} else {
				assert.Equal(t, tt.wantKey, body["prompt_cache_key"])
			}
			for _, field := range []string{"prompt_cache_options", "prompt_cache_breakpoint", "some_future_param"} {
				assert.NotContains(t, body, field)
			}
		})
	}
}

// TestResponses_CollapsesUpstreamStream covers the non-streaming path: the
// backend refuses stream:false, so GoModel streams and returns the final object.
func TestResponses_CollapsesUpstreamStream(t *testing.T) {
	srv, _ := providertest.SSEServer(t, codexSSE)
	provider := newTestProvider("token", srv.URL, srv.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-5.6-terra",
		Input: []core.ResponsesInputElement{{Type: "message", Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, "resp_1", resp.ID)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	assert.Equal(t, "ok", resp.Output[0].Content[0].Text)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 5, resp.Usage.TotalTokens)
}

// TestResponses_ReportsCachedTokens keeps the backend's prompt-cache hits
// visible: usage records and the dashboard read cached_tokens from the
// collapsed response, so dropping it would hide a cache regression.
func TestResponses_ReportsCachedTokens(t *testing.T) {
	sse := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-terra","output":[],"usage":{"input_tokens":19228,"input_tokens_details":{"cached_tokens":18176},"output_tokens":1,"total_tokens":19229}}}` + "\n\n" +
		"data: [DONE]\n\n"
	srv, _ := providertest.SSEServer(t, sse)
	provider := newTestProvider("token", srv.URL, srv.Client(), llmclient.Hooks{})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
	require.NoError(t, err)
	require.NotNil(t, resp.Usage)
	require.NotNil(t, resp.Usage.PromptTokensDetails)
	assert.Equal(t, 19228, resp.Usage.InputTokens)
	assert.Equal(t, 18176, resp.Usage.PromptTokensDetails.CachedTokens)
}

// TestResponses_TruncatedStreamIsAnError guards the non-streaming path against
// serving a stream that stopped early as an empty but successful answer: only a
// terminal lifecycle event may produce a response. Not to be confused with the
// response.incomplete terminal event, which is a legitimate response.
func TestResponses_TruncatedStreamIsAnError(t *testing.T) {
	created := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-terra"}}` + "\n\n"

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no events at all", body: "data: [DONE]\n\n", want: "ended before completion"},
		{name: "stream cut after response.created", body: created, want: "ended before completion"},
		{
			name: "upstream error event",
			body: created + "event: error\n" +
				`data: {"type":"error","code":"server_error","message":"upstream exploded"}` + "\n\n",
			want: "upstream exploded",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := providertest.SSEServer(t, tc.body)
			provider := newTestProvider("token", srv.URL, srv.Client(), llmclient.Hooks{})
			_, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestResponses_NonSuccessTerminalIsReturnedAsAResponse mirrors what the
// Responses API returns for a non-streaming call: a generation that failed or
// stopped short is a response whose status says so, not a transport error.
func TestResponses_NonSuccessTerminalIsReturnedAsAResponse(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		payload    string
		wantStatus string
		wantError  string // upstream error message the response must carry, "" for none
	}{
		{
			name:       "failed",
			event:      "response.failed",
			payload:    `{"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"gpt-5.6-terra","error":{"code":"server_error","message":"boom"}}}`,
			wantStatus: "failed",
			wantError:  "boom",
		},
		{
			name:       "incomplete",
			event:      "response.incomplete",
			payload:    `{"type":"response.incomplete","response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-5.6-terra","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
			wantStatus: "incomplete",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := providertest.SSEServer(t, "event: "+tc.event+"\ndata: "+tc.payload+"\n\n")
			provider := newTestProvider("token", srv.URL, srv.Client(), llmclient.Hooks{})
			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, resp.Status)
			if tc.wantError == "" {
				assert.Nil(t, resp.Error)
				return
			}
			require.NotNil(t, resp.Error)
			assert.Equal(t, tc.wantError, resp.Error.Message)
		})
	}
}

func TestStreamResponses_RequiresToken(t *testing.T) {
	provider := newTestProvider("", "http://example.invalid", http.DefaultClient, llmclient.Hooks{})
	_, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CHATGPT_API_KEY")
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		want       []string
	}{
		{name: "defaults", want: defaultModels},
		{name: "configured override", configured: []string{"gpt-5.6-terra"}, want: []string{"gpt-5.6-terra"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := New(providers.ProviderConfig{APIKey: "token"}, providers.ProviderOptions{Models: tc.configured})
			resp, err := provider.ListModels(context.Background())
			require.NoError(t, err)
			require.Len(t, resp.Data, len(tc.want))
			for i, model := range resp.Data {
				assert.Equal(t, tc.want[i], model.ID, "model[%d]", i)
			}
		})
	}
}

// TestUnsupportedSurfaces checks that surfaces the Codex backend does not
// implement report a capability gap (501) rather than a malformed request.
func TestUnsupportedSurfaces(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "token"}, providers.ProviderOptions{})
	calls := map[string]func() error{
		"ChatCompletion": func() error {
			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-terra"})
			return err
		},
		"StreamChatCompletion": func() error {
			_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-terra"})
			return err
		},
		"Embeddings": func() error {
			_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "gpt-5.6-terra"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusNotImplemented, gatewayErr.StatusCode)

			// The code is the programmatic half of the contract: callers
			// branch on it to tell a capability gap from a bad request.
			require.NotNil(t, gatewayErr.Code)
			assert.Equal(t, unsupportedOperationCode, *gatewayErr.Code)
		})
	}
}

func TestAccountIDFromToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "chatgpt token", token: tokenWithAccount(t, "acct-9"), want: "acct-9"},
		{name: "not a jwt", token: "sk-plain-key", want: ""},
		{name: "jwt without claim", token: "e30.e30.sig", want: ""},
		{name: "undecodable payload", token: "e30.!!!.sig", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, accountIDFromToken(tc.token))
		})
	}
}
