//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// straddlingChunks split "the secret answer" so the match crosses chunk
// boundaries: "sec" ends one delta and "ret" starts the next.
var straddlingChunks = []string{"the ", "sec", "ret ans", "wer"}

func TestPlugins_StreamPhase_E2E(t *testing.T) {
	fx := setupPluginServer(t)

	t.Run("replace transforms deltas across chunk boundaries", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(map[string]any{"stream_lookbehind": 64}), nil))
		fx.activate(t, workflowStep{Ref: "redact", Phase: "stream", Step: 1})

		resp := fx.chat(t, "hi", true)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

		chunks := readStreamingResponse(t, resp.Body)
		require.NotEmpty(t, chunks)
		assert.Equal(t, "the [redacted] answer", extractStreamContent(chunks))
		assert.True(t, chunks[len(chunks)-1].Done, "stream must end with [DONE]")
	})

	t.Run("warn passes the stream and sets the guardrail header", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "warn"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "stream", Step: 1})

		resp := fx.chat(t, "hi", true)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		chunks := readStreamingResponse(t, resp.Body)
		assert.Equal(t, "the secret answer", extractStreamContent(chunks))
		assert.True(t, chunks[len(chunks)-1].Done)
	})

	t.Run("block buffers and terminates with an error and content_filter", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block", "message": "leaked secret"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "stream", Step: 1})

		resp := fx.chat(t, "hi", true)
		require.Equal(t, http.StatusOK, resp.StatusCode, "the stream is already open when the decision is taken")
		body := readBody(t, resp)

		assert.NotContains(t, body, "secret answer", "forbidden text must never reach the client")
		assert.NotContains(t, body, `"content":"sec`)
		assert.Contains(t, body, `"code":"string_replace_match"`)
		assert.Contains(t, body, `"message":"leaked secret"`)
		assert.Contains(t, body, `"finish_reason":"content_filter"`)
		assert.True(t, strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]"), "stream must end with [DONE]: %s", body)
	})

	t.Run("respond buffers and streams the canned text only", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "respond", "message": "I cannot share that."}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "stream", Step: 1})

		resp := fx.chat(t, "hi", true)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		chunks := readStreamingResponse(t, resp.Body)
		assert.Equal(t, "I cannot share that.", extractStreamContent(chunks))
		assert.True(t, chunks[len(chunks)-1].Done)
	})

	t.Run("response phase block applies to streams too", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "the secret answer", straddlingChunks)
		fx.mustPutGuardrail(t, guardrailDef("scan", "string_replace", redactRules(map[string]any{"on_match": "block"}), nil))
		fx.activate(t, workflowStep{Ref: "scan", Phase: "response", Step: 1})

		resp := fx.chat(t, "hi", true)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body := readBody(t, resp)
		assert.NotContains(t, body, "secret answer")
		assert.Contains(t, body, `"code":"string_replace_match"`)
		assert.True(t, strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]"), body)
	})

	t.Run("responses API stream replace", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("redact", "string_replace", redactRules(nil), nil))
		fx.activate(t, workflowStep{Ref: "redact", Phase: "stream", Step: 1})

		// The mock echoes the input in 5-character deltas, so "secret" is
		// split across events.
		resp := fx.do(t, http.MethodPost, responsesPath, map[string]any{"model": "gpt-4", "input": "tell me the secret", "stream": true}, nil)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		events := readResponsesStream(t, resp.Body)
		assert.Equal(t, "Mock response to: tell me the [redacted]", extractResponsesStreamContent(events))
		assert.True(t, hasResponsesCompletedEvent(events))
		assert.True(t, hasResponsesDoneMarker(events))
	})
}
