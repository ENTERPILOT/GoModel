package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestTakeBodyTags(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantBody    string
		wantLabels  []string
		wantChanged bool
	}{
		{name: "no tags", body: `{"model":"m","metadata":{"env":"x"}}`, wantBody: `{"model":"m","metadata":{"env":"x"}}`},
		{name: "top-level list", body: `{"model":"m","tags":["a"," b ",3]}`, wantBody: `{"model":"m"}`, wantLabels: []string{"a", "b"}, wantChanged: true},
		{name: "list items are never split", body: `{"model":"m","tags":["team,west",""]}`, wantBody: `{"model":"m"}`, wantLabels: []string{"team,west"}, wantChanged: true},
		{name: "top-level string", body: `{"model":"m","tags":"a, b"}`, wantBody: `{"model":"m"}`, wantLabels: []string{"a", "b"}, wantChanged: true},
		{name: "metadata list beside other keys", body: `{"metadata":{"tags":["a"],"env":"x"},"model":"m"}`, wantBody: `{"metadata":{"env":"x"},"model":"m"}`, wantLabels: []string{"a"}, wantChanged: true},
		{name: "metadata holding only tags", body: `{"model":"m","metadata":{"tags":["a"]}}`, wantBody: `{"model":"m"}`, wantLabels: []string{"a"}, wantChanged: true},
		{name: "both", body: `{"model":"m","tags":["a"],"metadata":{"tags":["b"]}}`, wantBody: `{"model":"m"}`, wantLabels: []string{"a", "b"}, wantChanged: true},
		{name: "string metadata.tags is valid OpenAI metadata", body: `{"model":"m","metadata":{"tags":"a,b"}}`, wantBody: `{"model":"m","metadata":{"tags":"a,b"}}`},
		{name: "repeated metadata.tags keeps the last value", body: `{"model":"m","metadata":{"tags":["old"],"tags":"keep","env":"prod"}}`, wantBody: `{"model":"m","metadata":{"tags":["old"],"tags":"keep","env":"prod"}}`},
		{name: "repeated metadata.tags ending in a list", body: `{"model":"m","metadata":{"tags":"old","tags":["new"]}}`, wantBody: `{"model":"m"}`, wantLabels: []string{"new"}, wantChanged: true},
		{name: "not an object", body: `[{"tags":["a"]}]`, wantBody: `[{"tags":["a"]}]`},
		{name: "values kept byte for byte", body: `{"messages":[{"content":"<b>&</b>"}],"tags":["a"]}`, wantBody: `{"messages":[{"content":"<b>&</b>"}]}`, wantLabels: []string{"a"}, wantChanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, labels, changed := takeBodyTags([]byte(tt.body))
			assert.Equal(t, tt.wantChanged, changed)
			assert.JSONEq(t, tt.wantBody, string(body))
			assert.Equal(t, tt.wantLabels, labels)
			if tt.name == "values kept byte for byte" {
				assert.Contains(t, string(body), `"<b>&</b>"`)
			}
		})
	}
}

func postWithHeaders(t *testing.T, srv *Server, path, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLiteLLMTags_ChatBecomesLabels(t *testing.T) {
	provider := newRewriteTestProvider()
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true}}
	srv := New(provider, &Config{AuditLogger: auditLogger})

	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"tags":["eu"],"metadata":{"tags":["search","eu"],"env":"prod"}}`
	rec := postWithHeaders(t, srv, "/v1/chat/completions", body, http.Header{"X-Litellm-Tags": {"team-a, prod"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.NotNil(t, provider.capturedChatReq)
	assert.Equal(t, []string{"team-a", "prod", "eu", "search"}, core.RequestLabelsFromContext(provider.capturedChatCtx))
	forwarded, err := json.Marshal(provider.capturedChatReq)
	require.NoError(t, err)
	assert.NotContains(t, string(forwarded), `"tags"`, "providers reject LiteLLM tags: %s", forwarded)
	assert.Contains(t, string(forwarded), `"metadata":{"env":"prod"}`)
	assert.Contains(t, core.TaggingStripHeadersFromContext(provider.capturedChatCtx), "X-Litellm-Tags")

	require.NotEmpty(t, auditLogger.entries)
	entry, err := json.Marshal(auditLogger.entries[0])
	require.NoError(t, err)
	assert.Contains(t, string(entry), `"request_body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"tags":["eu"]`, "the audit entry keeps the body the client sent: %s", entry)
	assert.Equal(t, []string{"team-a", "prod", "eu", "search"}, auditLogger.entries[0].Data.Labels)
}

func TestLiteLLMTags_ResponsesAcceptListMetadataTags(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{})

	rec := postJSON(t, srv, "/v1/responses", `{"model":"gpt-4o-mini","input":"hi","metadata":{"tags":["search"]}}`)
	require.NotEqual(t, http.StatusBadRequest, rec.Code, "metadata.tags no longer fails decoding: %s", rec.Body.String())
	require.NotNil(t, provider.capturedResponsesReq)
	assert.Empty(t, provider.capturedResponsesReq.Metadata)
}

func TestLiteLLMTags_LargeBody(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{})

	// Past the 64KB inline snapshot limit, the body is read in full here.
	padding := strings.Repeat("x", 70*1024)
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` + padding + `"}],"tags":["big"]}`
	rec := postJSON(t, srv, "/v1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"big"}, core.RequestLabelsFromContext(provider.capturedChatCtx))
	content, _ := provider.capturedChatReq.Messages[0].Content.(string)
	assert.Equal(t, padding, content)
}

func TestLiteLLMTags_LeavesOtherRequestsAlone(t *testing.T) {
	provider := newRewriteTestProvider()
	srv := New(provider, &Config{})

	rec := postJSON(t, srv, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"metadata":{"tags":"a,b"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, core.RequestLabelsFromContext(provider.capturedChatCtx))
	forwarded, err := json.Marshal(provider.capturedChatReq)
	require.NoError(t, err)
	assert.Contains(t, string(forwarded), `"metadata":{"tags":"a,b"}`, "string metadata is OpenAI's and passes through")
	assert.Empty(t, core.TaggingStripHeadersFromContext(provider.capturedChatCtx))
}
