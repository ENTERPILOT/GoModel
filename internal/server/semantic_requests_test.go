package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestChatRequestFromSemanticEnvelope_CachesCanonicalRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-5-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hi"}],
			"response_format":{"type":"json_schema"}
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := chatRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	second, err := chatRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)
	require.NotNil(t, first.ExtraFields["response_format"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.ChatRequest)
	assert.Equal(t, "gpt-5-mini", env.SelectorHints.Model)
	assert.Equal(t, "openai", env.SelectorHints.Provider)
}

func TestResponsesRequestFromSemanticEnvelope_CachesCanonicalRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/responses",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-5-mini",
			"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}]
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	first, err := responsesRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	second, err := responsesRequestFromSemanticEnvelope(c)
	require.NoError(t, err)

	require.Same(t, first, second)

	input, ok := first.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	require.NotNil(t, input[0].ExtraFields["x_trace"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, first, env.ResponsesRequest)
	assert.Equal(t, "gpt-5-mini", env.SelectorHints.Model)
}

func TestEmbeddingRequestFromSemanticEnvelope_FallsBackToLiveBodyWhenIngressBodyMissing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{
		"model":"text-embedding-3-large",
		"provider":"openai",
		"input":"hello",
		"x_meta":{"trace":"abc"}
	}`))
	req.Header.Set("Content-Type", "application/json")

	frame := &core.IngressFrame{
		Method:          http.MethodPost,
		Path:            "/v1/embeddings",
		ContentType:     "application/json",
		RawBodyTooLarge: true,
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	c := e.NewContext(req, httptest.NewRecorder())

	embeddingReq, err := embeddingRequestFromSemanticEnvelope(c)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", embeddingReq.Model)
	require.Equal(t, "openai", embeddingReq.Provider)
	require.NotNil(t, embeddingReq.ExtraFields["x_meta"])

	env := core.GetSemanticEnvelope(c.Request().Context())
	require.NotNil(t, env)
	require.Same(t, embeddingReq, env.EmbeddingRequest)
	assert.True(t, env.JSONBodyParsed)
	assert.Equal(t, "text-embedding-3-large", env.SelectorHints.Model)
}
