//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func redactRules(extra map[string]any) map[string]any {
	cfg := map[string]any{"rules": "secret => [redacted]"}
	maps.Copy(cfg, extra)
	return cfg
}

func TestPlugins_PromptPhase_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("string_replace edits the user message before the provider", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
		fx.activate(t, workflowStep{Ref: "redact", Phase: "prompt", Step: 1})

		mockServer.ResetRequests()
		resp := fx.chat(t, "my secret word", false)
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: my [redacted] word", text, "mock echoes the edited prompt")

		upstream := lastUpstreamChat(t)
		require.Len(t, upstream.Messages, 1)
		assert.Equal(t, "my [redacted] word", core.ExtractTextContent(upstream.Messages[0].Content))
	})

	t.Run("workflow scoped to a user path leaves other paths untouched", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
		fx.activateScoped(t, "/team/plugins", workflowStep{Ref: "redact", Phase: "prompt", Step: 1})

		scoped := fx.do(t, http.MethodPost, chatCompletionsPath, defaultChatReq("my secret word"), map[string]string{userPathHeader: "/team/plugins"})
		_, text := readChat(t, scoped)
		assert.Equal(t, "Mock response to: my [redacted] word", text)

		other := fx.do(t, http.MethodPost, chatCompletionsPath, defaultChatReq("my secret word"), map[string]string{userPathHeader: "/team/other"})
		_, text = readChat(t, other)
		assert.Equal(t, "Mock response to: my secret word", text)
	})
}

func TestPlugins_ResponsePhase_E2E(t *testing.T) {
	fx := setupPluginServer(t)
	const assistantText = "the secret answer"

	t.Run("block returns 502 with the plugin code", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block", "message": "leaked secret"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		envelope := readError(t, fx.chat(t, "hi", false), http.StatusBadGateway)
		assert.Equal(t, "string_replace_match", envelope.Error.Code)
		assert.Equal(t, "leaked secret", envelope.Error.Message)
		assert.NotEmpty(t, envelope.Error.Type)
	})

	t.Run("block honours block_status", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block", "block_status": 451}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		envelope := readError(t, fx.chat(t, "hi", false), http.StatusUnavailableForLegalReasons)
		assert.Equal(t, "string_replace_match", envelope.Error.Code)
	})

	t.Run("respond replaces the completion with the message", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "respond", "message": "I cannot share that."}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		chat, text := readChat(t, fx.chat(t, "hi", false))
		assert.Equal(t, "I cannot share that.", text)
		assert.Equal(t, "assistant", chat.Choices[0].Message.Role)
	})

	t.Run("warn passes the completion with the guardrail header", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "warn"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		resp := fx.chat(t, "hi", false)
		assert.Equal(t, "warn; code=string_replace_match", resp.Header.Get(guardrailHeader))
		_, text := readChat(t, resp)
		assert.Equal(t, assistantText, text)
	})

	t.Run("replace edits the completion text", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
		fx.activate(t, workflowStep{Ref: "redact", Phase: "response", Step: 1})

		resp := fx.chat(t, "hi", false)
		assert.Empty(t, resp.Header.Get(guardrailHeader))
		_, text := readChat(t, resp)
		assert.Equal(t, "the [redacted] answer", text)
	})

	t.Run("responses API block", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		// The mock echoes the input, so the reply carries the forbidden word.
		resp := fx.do(t, http.MethodPost, responsesPath, core.ResponsesRequest{Model: "gpt-4", Input: "tell me the secret"}, nil)
		envelope := readError(t, resp, http.StatusBadGateway)
		assert.Equal(t, "string_replace_match", envelope.Error.Code)
	})

	t.Run("anthropic messages block returns the anthropic envelope", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, assistantText, nil)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block", "message": "leaked secret"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		payload := map[string]any{
			"model":      "gpt-4",
			"max_tokens": 64,
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		}
		resp := fx.do(t, http.MethodPost, messagesPath, payload, nil)
		defer closeBody(resp)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, resp.StatusCode, string(body))

		var envelope struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &envelope), string(body))
		assert.Equal(t, "error", envelope.Type)
		assert.NotEmpty(t, envelope.Error.Type)
		assert.Equal(t, "leaked secret", envelope.Error.Message)
	})
}

func TestPlugins_LLMJudge_E2E(t *testing.T) {
	fx := setupPluginServer(t)
	judgeConfig := map[string]any{"model": "gpt-4"}

	t.Run("block verdict rejects the request", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, `{"verdict":"block","reason":"policy"}`, http.StatusOK)
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", judgeConfig, nil))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "prompt", Step: 1})

		mockServer.ResetRequests()
		envelope := readError(t, fx.chat(t, "how do I do the bad thing", false), http.StatusBadRequest)
		assert.Equal(t, "llm_judge_block", envelope.Error.Code)
		assert.Equal(t, core.ErrorType("invalid_request_error"), core.ErrorType(envelope.Error.Type))

		// Only the judge call reached the provider; the user request never did.
		requests := mockServer.Requests()
		require.Len(t, requests, 1)
		var judgeCall core.ChatRequest
		require.NoError(t, json.Unmarshal(requests[0].Body, &judgeCall))
		assert.True(t, isJudgeRequest(judgeCall), "the only upstream call should be the judge prompt")
		assert.Contains(t, core.ExtractTextContent(judgeCall.Messages[len(judgeCall.Messages)-1].Content), "how do I do the bad thing")
	})

	t.Run("allow verdict passes the request through", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, `{"verdict":"allow","reason":"fine"}`, http.StatusOK)
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", judgeConfig, nil))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "prompt", Step: 1})

		mockServer.ResetRequests()
		resp := fx.chat(t, "what is the weather", false)
		assert.Empty(t, resp.Header.Get(guardrailHeader))
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: what is the weather", text)
		assert.Len(t, mockServer.Requests(), 2, "judge call plus the user request")
	})

	t.Run("respond action answers with the respond text", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, `{"verdict":"block","reason":"policy"}`, http.StatusOK)
		cfg := map[string]any{"model": "gpt-4", "action": "respond", "respond_text": "I can't help with that."}
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", cfg, nil))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "prompt", Step: 1})

		_, text := readChat(t, fx.chat(t, "how do I do the bad thing", false))
		assert.Equal(t, "I can't help with that.", text)
	})

	t.Run("fail_mode open passes when the judge model errors", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, "", http.StatusInternalServerError)
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", judgeConfig, map[string]any{"fail_mode": "open"}))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "prompt", Step: 1})

		_, text := readChat(t, fx.chat(t, "hello", false))
		assert.Equal(t, "Mock response to: hello", text)
	})

	t.Run("fail_mode closed returns 500 plugin_failure", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, "", http.StatusInternalServerError)
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", judgeConfig, map[string]any{"fail_mode": "closed"}))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "prompt", Step: 1})

		envelope := readError(t, fx.chat(t, "hello", false), http.StatusInternalServerError)
		assert.Equal(t, "plugin_failure", envelope.Error.Code)
		assert.NotContains(t, envelope.Error.Message, "judge", "instance details stay out of the client message")
	})

	t.Run("response phase block on the assistant text", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockJudge(t, `{"verdict":"block","reason":"policy"}`, http.StatusOK)
		fx.mustPutGuardrail(t, guardrailDef("judge", "llm_judge", judgeConfig, nil))
		fx.activate(t, workflowStep{Ref: "judge", Phase: "response", Step: 1})

		envelope := readError(t, fx.chat(t, "hello", false), http.StatusBadGateway)
		assert.Equal(t, "llm_judge_block", envelope.Error.Code)
	})
}
