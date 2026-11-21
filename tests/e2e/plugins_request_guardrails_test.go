//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// lastUpstreamBody returns the body of the most recent request the mock
// received on path.
func lastUpstreamBody(t *testing.T, path string) string {
	t.Helper()
	requests := mockServer.Requests()
	require.NotEmpty(t, requests, "mock received no requests")
	last := requests[len(requests)-1]
	require.Equal(t, path, last.Path)
	return string(last.Body)
}

// setupRequestGuardrails publishes a global workflow with one fixed step
// ("fixed", a system prompt) and two on-request steps: "redact" on the prompt
// and "scan", which blocks a response holding the word secret.
func setupRequestGuardrails(t *testing.T, fx *pluginFixture) {
	t.Helper()
	fx.mustPutGuardrail(t, guardrailDef("fixed", "system_prompt", map[string]any{"mode": "decorator", "content": "FIXED"}, nil))
	fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
	fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block", "message": "leaked secret"}), nil))
	fx.mustPutGuardrail(t, guardrailDef("unlisted", "string_replace", redactRules(nil), nil))
	fx.activate(t,
		workflowStep{Ref: "fixed", Phase: "prompt", Step: 1},
		workflowStep{Ref: "redact", Phase: "prompt", Step: 2, OnRequest: true},
		workflowStep{Ref: "scan", Phase: "response", Step: 1, OnRequest: true},
	)
	mockServer.ResetRequests()
}

func chatWithGuardrails(msg string, guardrails any) map[string]any {
	return map[string]any{
		"model":      "gpt-4",
		"messages":   []map[string]any{{"role": "user", "content": msg}},
		"guardrails": guardrails,
	}
}

func TestPlugins_RequestGuardrails_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("an on-request step stays off until the request names it", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		_, text := readChat(t, fx.chat(t, "my secret word", false))
		assert.Equal(t, "Mock response to: my secret word", text)

		upstream := lastUpstreamChat(t)
		require.Len(t, upstream.Messages, 2)
		assert.Equal(t, "FIXED", core.ExtractTextContent(upstream.Messages[0].Content), "the fixed step runs on every request")
		assert.Equal(t, "my secret word", core.ExtractTextContent(upstream.Messages[1].Content))
	})

	t.Run("chat completions adds the named guardrail and drops the field", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		resp := fx.do(t, http.MethodPost, chatCompletionsPath, chatWithGuardrails("my secret word", []string{"redact"}), nil)
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: my [redacted] word", text)

		body := lastUpstreamBody(t, "/chat/completions")
		assert.False(t, gjson.Get(body, "guardrails").Exists(), "the gateway field must not reach the provider: %s", body)
		upstream := lastUpstreamChat(t)
		require.Len(t, upstream.Messages, 2)
		assert.Equal(t, "FIXED", core.ExtractTextContent(upstream.Messages[0].Content), "a request cannot drop a fixed step")
		assert.Equal(t, "my [redacted] word", core.ExtractTextContent(upstream.Messages[1].Content))
	})

	t.Run("streaming chat completions adds the named guardrail", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		payload := chatWithGuardrails("my secret word", []string{"redact"})
		payload["stream"] = true
		resp := fx.do(t, http.MethodPost, chatCompletionsPath, payload, nil)
		body := readBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)

		upstreamBody := lastUpstreamBody(t, "/chat/completions")
		assert.False(t, gjson.Get(upstreamBody, "guardrails").Exists(), upstreamBody)
		assert.Equal(t, "my [redacted] word", gjson.Get(upstreamBody, "messages.1.content").String())
	})

	t.Run("responses adds the named guardrail and drops the field", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		resp := fx.do(t, http.MethodPost, responsesPath, map[string]any{
			"model":      "gpt-4",
			"input":      "my secret word",
			"guardrails": []string{"redact"},
		}, nil)
		body := readBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)

		upstreamBody := lastUpstreamBody(t, "/responses")
		assert.False(t, gjson.Get(upstreamBody, "guardrails").Exists(), upstreamBody)
		assert.Contains(t, upstreamBody, "my [redacted] word")
		assert.NotContains(t, upstreamBody, "my secret word")
		assert.Equal(t, "FIXED", gjson.Get(upstreamBody, "instructions").String())
	})

	t.Run("messages adds the named guardrail and drops the field", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		resp := fx.do(t, http.MethodPost, messagesPath, map[string]any{
			"model":      "gpt-4",
			"max_tokens": 64,
			"messages":   []map[string]any{{"role": "user", "content": "my secret word"}},
			"guardrails": []string{"redact"},
		}, nil)
		body := readBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Contains(t, body, "my [redacted] word")

		upstreamBody := lastUpstreamBody(t, "/chat/completions")
		assert.False(t, gjson.Get(upstreamBody, "guardrails").Exists(), upstreamBody)
		assert.NotContains(t, upstreamBody, "my secret word")
		assert.Contains(t, upstreamBody, "FIXED")
	})

	t.Run("a response-phase guardrail runs only for the request that names it", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)
		scriptMockChat(t, "the secret answer", nil)

		_, text := readChat(t, fx.chat(t, "hi", false))
		assert.Equal(t, "the secret answer", text)

		resp := fx.do(t, http.MethodPost, chatCompletionsPath, chatWithGuardrails("hi", []string{"scan"}), nil)
		envelope := readError(t, resp, http.StatusBadGateway)
		assert.Equal(t, "string_replace_match", envelope.Error.Code)
		assert.Equal(t, "leaked secret", envelope.Error.Message)
	})

	t.Run("a stream-phase guardrail joins the stream only when named", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("redact-out", "string_replace", redactRules(map[string]any{"stream_lookbehind": 64}), nil))
		fx.activate(t, workflowStep{Ref: "redact-out", Phase: "stream", Step: 1, OnRequest: true})

		plain := fx.chat(t, "hi", true)
		defer closeBody(plain)
		require.Equal(t, http.StatusOK, plain.StatusCode)
		assert.Equal(t, "the secret answer", extractStreamContent(readStreamingResponse(t, plain.Body)))

		payload := chatWithGuardrails("hi", []string{"redact-out"})
		payload["stream"] = true
		named := fx.do(t, http.MethodPost, chatCompletionsPath, payload, nil)
		defer closeBody(named)
		require.Equal(t, http.StatusOK, named.StatusCode)
		chunks := readStreamingResponse(t, named.Body)
		require.NotEmpty(t, chunks)
		assert.Equal(t, "the [redacted] answer", extractStreamContent(chunks))
		assert.True(t, chunks[len(chunks)-1].Done, "stream must end with [DONE]")
	})

	t.Run("naming a fixed guardrail changes nothing", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		resp := fx.do(t, http.MethodPost, chatCompletionsPath, chatWithGuardrails("my secret word", []string{"fixed"}), nil)
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: my secret word", text)
		assert.False(t, gjson.Get(lastUpstreamBody(t, "/chat/completions"), "guardrails").Exists())
	})

	t.Run("an empty list or null names nothing and is still dropped", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		for _, value := range []any{[]string{}, nil} {
			resp := fx.do(t, http.MethodPost, chatCompletionsPath, chatWithGuardrails("my secret word", value), nil)
			_, text := readChat(t, resp)
			assert.Equal(t, "Mock response to: my secret word", text)
			assert.False(t, gjson.Get(lastUpstreamBody(t, "/chat/completions"), "guardrails").Exists())
		}
	})

	t.Run("a guardrail the workflow does not offer is refused before any provider call", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		tests := []struct {
			name       string
			guardrails any
			want       string
		}{
			{"unknown name", []string{"nope"}, `guardrail "nope" cannot be selected for this request`},
			{"defined but not in the workflow", []string{"unlisted"}, `guardrail "unlisted" cannot be selected for this request`},
			{"one bad name among good ones", []string{"redact", "nope"}, `guardrail "nope" cannot be selected for this request`},
			{"not a list", "redact", "guardrails must be an array of guardrail names"},
			{"not names", []any{map[string]any{"redact": map[string]any{}}}, "guardrails must be an array of guardrail names"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				mockServer.ResetRequests()
				for _, target := range []struct {
					path    string
					payload map[string]any
				}{
					{chatCompletionsPath, chatWithGuardrails("my secret word", tt.guardrails)},
					{responsesPath, map[string]any{"model": "gpt-4", "input": "my secret word", "guardrails": tt.guardrails}},
				} {
					envelope := readError(t, fx.do(t, http.MethodPost, target.path, target.payload, nil), http.StatusBadRequest)
					assert.Equal(t, "invalid_request_error", envelope.Error.Type, target.path)
					assert.Equal(t, tt.want, envelope.Error.Message, target.path)
				}

				resp := fx.do(t, http.MethodPost, messagesPath, map[string]any{
					"model":      "gpt-4",
					"max_tokens": 64,
					"messages":   []map[string]any{{"role": "user", "content": "my secret word"}},
					"guardrails": tt.guardrails,
				}, nil)
				defer closeBody(resp)
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
				assert.Equal(t, "error", gjson.GetBytes(body, "type").String(), "messages answers in the Anthropic envelope")
				assert.Equal(t, tt.want, gjson.GetBytes(body, "error.message").String())

				assert.Empty(t, mockServer.Requests(), "a refused request must not reach the provider")
			})
		}
	})

	t.Run("the unavailable error carries the guardrails param and code", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		setupRequestGuardrails(t, fx)

		resp := fx.do(t, http.MethodPost, chatCompletionsPath, chatWithGuardrails("hi", []string{"nope"}), nil)
		defer closeBody(resp)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
		assert.Equal(t, "guardrails", gjson.GetBytes(body, "error.param").String())
		assert.Equal(t, "guardrail_not_available", gjson.GetBytes(body, "error.code").String())
	})

	t.Run("only the workflow a request matches decides what it may add", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
		fx.activateScoped(t, "/team/plugins", workflowStep{Ref: "redact", Phase: "prompt", Step: 1, OnRequest: true})
		payload := chatWithGuardrails("my secret word", []string{"redact"})

		scoped := fx.do(t, http.MethodPost, chatCompletionsPath, payload, map[string]string{userPathHeader: "/team/plugins"})
		_, text := readChat(t, scoped)
		assert.Equal(t, "Mock response to: my [redacted] word", text)

		other := fx.do(t, http.MethodPost, chatCompletionsPath, payload, map[string]string{userPathHeader: "/team/other"})
		envelope := readError(t, other, http.StatusBadRequest)
		assert.Equal(t, `guardrail "redact" cannot be selected for this request`, envelope.Error.Message)
	})
}
