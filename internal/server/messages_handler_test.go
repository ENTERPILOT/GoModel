package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestMessages_NonStreaming(t *testing.T) {
	provider := &capturingProvider{
		supportedModels: []string{"claude-test"},
		response: &core.ChatResponse{
			ID:     "resp-1",
			Object: "chat.completion",
			Model:  "claude-test",
			Choices: []core.Choice{{
				Index:        0,
				Message:      core.ResponseMessage{Role: "assistant", Content: "Hello back"},
				FinishReason: "stop",
			}},
			Usage: core.Usage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12},
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[map[string]any](t, rec)
	assert.Equal(t, "message", resp["type"])
	assert.Equal(t, "assistant", resp["role"], "envelope = %+v", resp)

	content, _ := resp["content"].([]any)
	require.Len(t, content, 1)

	block, _ := content[0].(map[string]any)
	assert.Equal(t, "text", block["type"])
	assert.Equal(t, "Hello back", block["text"], "content block = %+v", block)
	assert.Equal(t, "end_turn", resp["stop_reason"])

	// The Anthropic request must have been translated to the canonical chat type.
	require.NotNil(t, provider.capturedChatReq)
	require.Equal(t, core.RequestDialectAnthropicMessages, core.RequestDialectFromContext(provider.capturedChatCtx))

	msgs := provider.capturedChatReq.Messages
	require.Len(t, msgs, 2)
	require.Equal(t, "system", msgs[0].Role)
	require.Equal(t, "user", msgs[1].Role)
}

func TestMessages_Streaming(t *testing.T) {
	chatSSE := strings.Join([]string{
		`data: {"id":"resp-2","model":"claude-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Hi!"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	provider := &capturingProvider{
		supportedModels: []string{"claude-test"},
		streamData:      chatSSE,
	}

	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text_delta"`,
		"event: message_delta",
		"event: message_stop",
	} {
		assert.Contains(t, body, want)
	}
	// include_usage must be set so the converter sees the final usage chunk.
	require.NotNil(t, provider.capturedChatReq)
	require.NotNil(t, provider.capturedChatReq.StreamOptions)
	assert.True(t, provider.capturedChatReq.StreamOptions.IncludeUsage)
}

func TestMessages_NativeAnthropicForwarding(t *testing.T) {
	nativeResponse := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	provider := &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(nativeResponse)),
		},
	}

	handler := NewHandler(provider, nil, nil, nil)

	// cache_control does not survive the translated pipeline; the native path
	// must forward it verbatim.
	reqBody := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody, echotest.WithHeader("anthropic-beta", "claude-code-20250219"))
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, nativeResponse, rec.Body.String())
	require.Equal(t, "anthropic", provider.lastPassthroughProvider)

	forwarded := provider.lastPassthroughReq
	require.NotNil(t, forwarded)
	assert.Equal(t, "messages", forwarded.Endpoint)
	assert.Equal(t, http.MethodPost, forwarded.Method)

	forwardedBody, err := io.ReadAll(forwarded.Body)
	require.NoError(t, err)
	assert.Equal(t, reqBody, string(forwardedBody), "forwarded body = %s, want original request verbatim", forwardedBody)
	assert.Equal(t, "claude-code-20250219", forwarded.Headers.Get("anthropic-beta"))
}

func TestMessages_NativeForwardingSurvivesUntranslatableContent(t *testing.T) {
	// Server-tool history (Claude Code's WebSearch) has no canonical
	// equivalent. On an Anthropic route the body is forwarded verbatim, so
	// the translation gap must not fail the request.
	nativeResponse := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	provider := &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(nativeResponse)),
		},
	}
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search"},{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},{"role":"user","content":"and?"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.lastPassthroughReq)

	forwardedBody, err := io.ReadAll(provider.lastPassthroughReq.Body)
	require.NoError(t, err)
	assert.Equal(t, reqBody, string(forwardedBody), "forwarded body = %s, want original request verbatim", forwardedBody)
}

// A turn produced by another provider replays a thinking block Anthropic never
// signed. Forwarding it verbatim would fail the request upstream, so the
// request takes the translated pipeline, which drops the block.
func TestMessages_UnsignedThinkingSkipsNativeForwarding(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "anthropic"},
		response: &core.ChatResponse{
			ID:      "msg_1",
			Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		},
	}
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"foreign","signature":""},{"type":"text","text":"391"}]},` +
		`{"role":"user","content":"and now?"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Nil(t, provider.lastPassthroughReq)
	assert.Contains(t, rec.Body.String(), `"text":"ok"`)
}

// Untranslatable content wins over the unsigned-thinking detour: the
// translated pipeline would reject the request outright, so the body is still
// forwarded verbatim and Anthropic decides.
func TestMessages_UnsignedThinkingKeepsNativeForwardingForUntranslatableContent(t *testing.T) {
	nativeResponse := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	provider := &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "anthropic"},
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(nativeResponse)),
		},
	}
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"foreign","signature":""},{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},` +
		`{"role":"user","content":"and?"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, provider.lastPassthroughReq)

	forwardedBody, err := io.ReadAll(provider.lastPassthroughReq.Body)
	require.NoError(t, err)
	assert.Equal(t, reqBody, string(forwardedBody), "forwarded body = %s, want original request verbatim", forwardedBody)
}

func TestMessages_TranslatedPipelineStillRejectsUntranslatableContent(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-test"},
		providerTypes:   map[string]string{"gpt-test": "openai"},
	}
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file_1"}]}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "container_upload")
	assert.Nil(t, provider.lastPassthroughReq)
}

func TestRewriteMessagesModel(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		model string
		want  string
	}{
		{
			name:  "same model leaves body untouched",
			body:  `{"model":"claude-test","max_tokens":1,"messages":[]}`,
			model: "claude-test",
			want:  `{"model":"claude-test","max_tokens":1,"messages":[]}`,
		},
		{
			name:  "empty model leaves body untouched",
			body:  `{"model":"claude-test"}`,
			model: "",
			want:  `{"model":"claude-test"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rewriteMessagesModel([]byte(tt.body), tt.model)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got), "body = %s, want %s", got, tt.want)
		})
	}

	t.Run("alias rewrites only the model value", func(t *testing.T) {
		// Whitespace, member order, and numeric spelling elsewhere must
		// survive the rewrite untouched.
		body := `{"max_tokens": 1,  "model" : "my-alias" , "temperature": 1.50}`
		want := `{"max_tokens": 1,  "model" : "claude-test" , "temperature": 1.50}`
		got, err := rewriteMessagesModel([]byte(body), "claude-test")
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	})

	t.Run("duplicate model members rewrite the last one", func(t *testing.T) {
		// Decoders keep the last duplicate member, so the rewrite must target
		// it — rewriting the first would leave the effective model unchanged.
		body := `{"model":"ignored","max_tokens":1,"model":"my-alias"}`
		want := `{"model":"ignored","max_tokens":1,"model":"claude-test"}`
		got, err := rewriteMessagesModel([]byte(body), "claude-test")
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	})

	t.Run("non-object body errors", func(t *testing.T) {
		_, err := rewriteMessagesModel([]byte(`[1,2]`), "claude-test")
		require.Error(t, err)
	})
}

func TestMessages_InvalidRequestReturnsAnthropicError(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"claude-test"}}
	handler := NewHandler(provider, nil, nil, nil)

	// max_tokens is required by the Anthropic dialect.
	reqBody := `{"model":"claude-test","messages":[{"role":"user","content":"Hi"}]}`
	c, rec := echotest.Post(t, "/v1/messages", reqBody)
	err := handler.Messages(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	resp := echotest.Decode[map[string]any](t, rec)
	require.Equal(t, "error", resp["type"], "response is not an Anthropic error envelope: %+v", resp)

	errObj, _ := resp["error"].(map[string]any)
	assert.Equal(t, "invalid_request_error", errObj["type"])
}

func TestCountMessageTokens(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"claude-test"}}
	handler := NewHandler(provider, nil, nil, nil)

	reqBody := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"count these tokens please"}]}`
	c, rec := echotest.Post(t, "/v1/messages/count_tokens", reqBody)
	err := handler.CountMessageTokens(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[map[string]any](t, rec)

	tokens, ok := resp["input_tokens"].(float64)
	assert.True(t, ok)
	assert.Greater(t, tokens, float64(0))
}

type tokenCountingMockProvider struct {
	*mockProvider
	count    int
	countErr error
	calls    int
}

func (m *tokenCountingMockProvider) CountMessagesTokens(_ context.Context, _ string, _ []byte) (int, error) {
	m.calls++
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.count, nil
}

// When the route can count tokens itself the answer is exact; when it cannot,
// or the upstream call fails, the heuristic estimate still answers so the
// endpoint never stops working.
func TestCountMessageTokens_ProviderBacked(t *testing.T) {
	body := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"count these tokens please"}]}`
	call := func(t *testing.T, provider core.RoutableProvider) float64 {
		t.Helper()
		handler := NewHandler(provider, nil, nil, nil)
		c, rec := echotest.Post(t, "/v1/messages/count_tokens", body)
		err := handler.CountMessageTokens(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		resp := echotest.Decode[map[string]any](t, rec)

		return resp["input_tokens"].(float64)
	}

	exact := &tokenCountingMockProvider{mockProvider: &mockProvider{supportedModels: []string{"claude-test"}}, count: 4321}
	assert.Equal(t, float64(4321), call(t, exact))
	assert.Equal(t, 1, exact.calls)

	heuristic := call(t, &mockProvider{supportedModels: []string{"claude-test"}})
	assert.Greater(t, heuristic, float64(0))
	assert.NotEqual(t, float64(4321), heuristic)

	failing := &tokenCountingMockProvider{mockProvider: &mockProvider{supportedModels: []string{"claude-test"}}, countErr: errors.New("upstream down")}
	assert.Equal(t, heuristic, call(t, failing))
}
