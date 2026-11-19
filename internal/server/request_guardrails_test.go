package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// The native Messages path forwards the client's bytes, so a body that
// carries the gateway's guardrails field never takes it: the field would
// reach Anthropic, which rejects unknown members.
func TestMessages_GuardrailsFieldIsNeverForwardedNatively(t *testing.T) {
	newProvider := func() *mockProvider {
		return &mockProvider{
			supportedModels: []string{"claude-fable-5"},
			providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
			response: &core.ChatResponse{
				ID:      "chatcmpl-1",
				Model:   "claude-fable-5",
				Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
			},
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-fable-5","usage":{"input_tokens":1,"output_tokens":1}}`)),
			},
		}
	}
	body := func(guardrails string) string {
		return `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]` + guardrails + `}`
	}

	t.Run("without the field the request is forwarded natively", func(t *testing.T) {
		provider := newProvider()
		c, rec := echotest.Post(t, "/v1/messages", body(""))
		require.NoError(t, NewHandler(provider, nil, nil, nil).Messages(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotNil(t, provider.lastPassthroughReq)
	})

	for name, field := range map[string]string{"empty list": `,"guardrails":[]`, "null": `,"guardrails":null`} {
		t.Run(name+" is translated", func(t *testing.T) {
			provider := newProvider()
			c, rec := echotest.Post(t, "/v1/messages", body(field))
			require.NoError(t, NewHandler(provider, nil, nil, nil).Messages(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Nil(t, provider.lastPassthroughReq, "the body with the gateway field must not be forwarded as sent")
			assert.Equal(t, 1, provider.chatCompletionCalls)
		})
	}

	t.Run("a named guardrail that cannot run is refused", func(t *testing.T) {
		provider := newProvider()
		c, rec := echotest.Post(t, "/v1/messages", body(`,"guardrails":["pii"]`))
		require.NoError(t, NewHandler(provider, nil, nil, nil).Messages(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `guardrail \"pii\" cannot be selected for this request`)
		assert.Nil(t, provider.lastPassthroughReq)
		assert.Zero(t, provider.chatCompletionCalls)
	})
}

// A count that names guardrails stands for an inference request that would
// run them over the prompt first, so the prompt stays local and the estimate
// answers.
func TestCountMessageTokens_NamedGuardrailsKeepThePromptLocal(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		wantRemote bool
	}{
		{name: "named guardrail", field: `,"guardrails":["redact-pii"]`},
		{name: "no field", field: ``, wantRemote: true},
		{name: "null", field: `,"guardrails":null`, wantRemote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &chatTokenCountingMockProvider{
				mockProvider: &mockProvider{supportedModels: []string{"gpt-test"}},
				messagesErr:  core.ErrMessagesTokenCountUnsupported,
				chatCount:    777,
			}
			body := `{"model":"gpt-test","messages":[{"role":"user","content":"my email is jane@example.com"}]` + tt.field + `}`
			c, rec := echotest.Post(t, "/v1/messages/count_tokens", body)

			require.NoError(t, NewHandler(provider, nil, nil, nil).CountMessageTokens(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			count := echotest.Decode[map[string]any](t, rec)["input_tokens"].(float64)
			if tt.wantRemote {
				assert.Equal(t, float64(777), count)
				require.NotNil(t, provider.last)
				assert.Nil(t, provider.last.ExtraFields.Lookup("guardrails"))
				return
			}
			assert.NotEqual(t, float64(777), count)
			assert.Zero(t, provider.messagesCalls, "the prompt must not reach the native Messages counter")
			assert.Zero(t, provider.chatCalls, "the prompt must not reach the chat counter")
		})
	}
}

// The Responses utility routes send the prompt to the provider as well, so
// they read the guardrails field like POST /v1/responses does.
func TestResponsesUtilityRoutes_RequestGuardrails(t *testing.T) {
	for _, path := range []string{"/v1/responses/input_tokens", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			provider := &mockProvider{
				supportedModels: []string{"gpt-5-mini"},
				providerTypes:   map[string]string{"gpt-5-mini": "mock"},
			}
			handler := NewHandler(provider, nil, nil, nil)
			call := handler.ResponseInputTokens
			if strings.HasSuffix(path, "/compact") {
				call = handler.CompactResponse
			}

			c, rec := echotest.Post(t, path, `{"model":"gpt-5-mini","input":"hello","guardrails":["pii"]}`)
			require.NoError(t, call(c))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			envelope := echotest.Decode[core.OpenAIErrorEnvelope](t, rec)
			assert.Equal(t, `guardrail "pii" cannot be selected for this request`, envelope.Error.Message)
			assert.Empty(t, provider.capturedResponseUtilityReqs, "a refused request must not reach the provider")

			c, rec = echotest.Post(t, path, `{"model":"gpt-5-mini","input":"hello","guardrails":[],"user":"u"}`)
			require.NoError(t, call(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, provider.capturedResponseUtilityReqs, 1)
			assert.Nil(t, provider.capturedResponseUtilityReqs[0].ExtraFields.Lookup("guardrails"), "the field must not reach the provider")
		})
	}
}
