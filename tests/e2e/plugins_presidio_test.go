//go:build e2e

package e2e

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// fakeAnalyzer stands in for a Presidio analyzer: it reports e-mail
// addresses and the name "Ann Lee" with code-point offsets.
func fakeAnalyzer(t *testing.T) *httptest.Server {
	t.Helper()
	emailRe := regexp.MustCompile(`[a-z]+@[a-z]+\.[a-z]+`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" {
			_, _ = w.Write([]byte(`["PERSON","EMAIL_ADDRESS"]`))
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type result struct {
			EntityType string  `json:"entity_type"`
			Start      int     `json:"start"`
			End        int     `json:"end"`
			Score      float64 `json:"score"`
		}
		results := []result{}
		add := func(entity string, start, end int) {
			results = append(results, result{entity, utf8.RuneCountInString(req.Text[:start]), utf8.RuneCountInString(req.Text[:end]), 0.9})
		}
		for _, loc := range emailRe.FindAllStringIndex(req.Text, -1) {
			add("EMAIL_ADDRESS", loc[0], loc[1])
		}
		if i := strings.Index(req.Text, "Ann Lee"); i >= 0 {
			add("PERSON", i, i+len("Ann Lee"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func presidioConfig(analyzerURL string, extra map[string]any) map[string]any {
	cfg := map[string]any{"analyzer_url": analyzerURL, "restore": true}
	maps.Copy(cfg, extra)
	return cfg
}

func TestPlugins_Presidio_E2E(t *testing.T) {
	fx := setupPluginServer(t)
	analyzer := fakeAnalyzer(t)
	const userText = "I am Ann Lee, mail ann@example.com, greet me"

	t.Run("prompt is anonymized for the provider and restored for the client", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("pii-in", "presidio", presidioConfig(analyzer.URL, nil), nil))
		fx.mustPutGuardrail(t, guardrailDef("pii-out", "presidio", presidioConfig(analyzer.URL, nil), nil))
		fx.activate(t, workflowStep{Ref: "pii-in", Phase: "prompt", Step: 1}, workflowStep{Ref: "pii-out", Phase: "response", Step: 1})

		mockServer.ResetRequests()
		resp := fx.chat(t, userText, false)
		_, text := readChat(t, resp)
		assert.Equal(t, "Mock response to: "+userText, text, "the client sees its own values")

		upstream := lastUpstreamChat(t)
		require.Len(t, upstream.Messages, 1)
		assert.Equal(t, "I am <PERSON_1>, mail <EMAIL_ADDRESS_1>, greet me", core.ExtractTextContent(upstream.Messages[0].Content))
	})

	t.Run("stream is restored in flight across chunk boundaries", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockChat(t, "", []string{"Hi <PERS", "ON_1>, I will write to ", "<EMAIL_ADDRESS_1> and to bob@example.com", " now"})
		fx.mustPutGuardrail(t, guardrailDef("pii-in", "presidio", presidioConfig(analyzer.URL, nil), nil))
		fx.mustPutGuardrail(t, guardrailDef("pii-out", "presidio", presidioConfig(analyzer.URL, map[string]any{"stream_chunk": 8}), nil))
		fx.activate(t, workflowStep{Ref: "pii-in", Phase: "prompt", Step: 1}, workflowStep{Ref: "pii-out", Phase: "stream", Step: 1})

		resp := fx.chat(t, userText, true)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		chunks := readStreamingResponse(t, resp.Body)
		require.NotEmpty(t, chunks)
		assert.Equal(t, "Hi Ann Lee, I will write to ann@example.com and to <EMAIL_ADDRESS_2> now", extractStreamContent(chunks))
		assert.True(t, chunks[len(chunks)-1].Done)
	})

	t.Run("streamed tool-call arguments are restored in flight", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		scriptMockToolStream(t, "send_email", []string{`{"to":"<EMAIL_ADD`, `RESS_1>","body":"Hi <PERS`, `ON_1>"}`})
		fx.mustPutGuardrail(t, guardrailDef("pii-in", "presidio", presidioConfig(analyzer.URL, nil), nil))
		fx.mustPutGuardrail(t, guardrailDef("pii-out", "presidio", presidioConfig(analyzer.URL, map[string]any{"stream_chunk": 8, "stream_lookbehind": 16}), nil))
		fx.activate(t, workflowStep{Ref: "pii-in", Phase: "prompt", Step: 1}, workflowStep{Ref: "pii-out", Phase: "stream", Step: 1})

		resp := fx.chat(t, userText, true)
		defer closeBody(resp)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		chunks := readStreamingResponse(t, resp.Body)
		require.NotEmpty(t, chunks)
		args, id, name := "", "", ""
		for _, chunk := range chunks {
			for _, choice := range chunk.Choices {
				delta, _ := choice["delta"].(map[string]any)
				calls, _ := delta["tool_calls"].([]any)
				for _, c := range calls {
					call, _ := c.(map[string]any)
					if v, _ := call["id"].(string); v != "" {
						id = v
					}
					fn, _ := call["function"].(map[string]any)
					if v, _ := fn["name"].(string); v != "" {
						name = v
					}
					v, _ := fn["arguments"].(string)
					args += v
				}
			}
		}
		assert.Equal(t, "call_1", id)
		assert.Equal(t, "send_email", name)
		assert.Equal(t, `{"to":"ann@example.com","body":"Hi Ann Lee"}`, args)
		assert.True(t, chunks[len(chunks)-1].Done)
	})

	t.Run("blocking entity rejects the prompt", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("pii-in", "presidio", presidioConfig(analyzer.URL, map[string]any{"block_entities": []string{"EMAIL_ADDRESS"}, "message": "no e-mail addresses"}), nil))
		fx.activate(t, workflowStep{Ref: "pii-in", Phase: "prompt", Step: 1})

		envelope := readError(t, fx.chat(t, userText, false), http.StatusBadRequest)
		assert.Equal(t, "presidio_blocked_entity", envelope.Error.Code)
		assert.Equal(t, "no e-mail addresses", envelope.Error.Message)
	})

	t.Run("unreachable analyzer fails closed and shows as degraded", func(t *testing.T) {
		t.Cleanup(func() { fx.reset(t) })
		fx.mustPutGuardrail(t, guardrailDef("pii-in", "presidio", presidioConfig("http://127.0.0.1:1", nil), nil))
		fx.activate(t, workflowStep{Ref: "pii-in", Phase: "prompt", Step: 1})

		readError(t, fx.chat(t, userText, false), http.StatusInternalServerError)

		var views []struct {
			Name   string `json:"name"`
			Health string `json:"health"`
		}
		fx.adminJSON(t, http.MethodGet, adminGuardrailsPath, nil, http.StatusOK, &views)
		require.Len(t, views, 1)
		assert.Equal(t, "degraded", views[0].Health)
	})
}

// scriptMockToolStream makes the shared mock answer streaming chat
// completions with one tool call whose arguments arrive in the given
// chunks: the first chunk carries the call's id and name, the last the
// finish_reason.
func scriptMockToolStream(t *testing.T, name string, argChunks []string) {
	t.Helper()
	mockServer.SetCustomHandler(func(w http.ResponseWriter, r *http.Request) bool {
		req, ok := decodeMockChat(r)
		if !ok || !req.Stream {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i, chunk := range argChunks {
			call := map[string]any{"index": 0, "function": map[string]any{"arguments": chunk}}
			if i == 0 {
				call["id"] = "call_1"
				call["type"] = "function"
				call["function"] = map[string]any{"name": name, "arguments": chunk}
			}
			choice := map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": nil}
			if i == len(argChunks)-1 {
				choice["finish_reason"] = "tool_calls"
			}
			data, _ := json.Marshal(map[string]any{"id": "chatcmpl-tool-stream", "object": "chat.completion.chunk", "model": req.Model, "created": 1, "choices": []any{choice}})
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		return true
	})
	t.Cleanup(resetMock)
}
