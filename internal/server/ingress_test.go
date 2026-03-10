package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type explodingReadCloser struct{}

func (r *explodingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("body should not be read")
}

func (r *explodingReadCloser) Close() error {
	return nil
}

func TestIngressCapture_SetsFrameAndSemanticEnvelope(t *testing.T) {
	e := echo.New()

	reqBody := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?foo=bar", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.IngressFrame
	var capturedEnv *core.SemanticEnvelope
	var downstreamBody string

	handler := IngressCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetIngressFrame(c.Request().Context())
		capturedEnv = core.GetSemanticEnvelope(c.Request().Context())
		bodyBytes, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		downstreamBody = string(bodyBytes)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Equal(t, http.MethodPost, capturedFrame.Method)
	assert.Equal(t, "/v1/chat/completions", capturedFrame.Path)
	assert.Equal(t, "application/json", capturedFrame.ContentType)
	assert.Equal(t, "req-123", capturedFrame.RequestID)
	assert.Equal(t, []string{"bar"}, capturedFrame.QueryParams["foo"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", capturedFrame.TraceMetadata["Traceparent"])
	assert.JSONEq(t, reqBody, string(capturedFrame.RawBody))
	assert.JSONEq(t, reqBody, downstreamBody)

	require.NotNil(t, capturedEnv)
	assert.Equal(t, "openai_compat", capturedEnv.Dialect)
	assert.Equal(t, "chat_completions", capturedEnv.Operation)
	assert.Equal(t, "gpt-5-mini", capturedEnv.SelectorHints.Model)
	assert.True(t, capturedEnv.JSONBodyParsed)
	assert.Empty(t, capturedEnv.OpaqueJSONFields)
}

func TestIngressCapture_PreservesPassthroughRouteParams(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "provider", Value: "openai"},
		{Name: "endpoint", Value: "responses"},
	})

	var capturedFrame *core.IngressFrame
	var capturedEnv *core.SemanticEnvelope

	handler := IngressCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetIngressFrame(c.Request().Context())
		capturedEnv = core.GetSemanticEnvelope(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Equal(t, "openai", capturedFrame.RouteParams["provider"])
	assert.Equal(t, "responses", capturedFrame.RouteParams["endpoint"])

	require.NotNil(t, capturedEnv)
	assert.Equal(t, "provider_passthrough", capturedEnv.Dialect)
	assert.Equal(t, "openai", capturedEnv.SelectorHints.Provider)
	assert.Equal(t, "responses", capturedEnv.SelectorHints.Endpoint)
}

func TestModelValidation_UsesSemanticEnvelopeWithoutReadingBody(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-123")
	req.Body = &explodingReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		RawBody:     []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
		RequestID:   "req-123",
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := ModelValidation(provider)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestIngressCapture_SkipsOversizedBodies(t *testing.T) {
	e := echo.New()

	largeContent := strings.Repeat("x", int(auditlog.MaxBodyCapture)+128)
	reqBody := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"` + largeContent + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.IngressFrame
	var downstreamBody string

	handler := IngressCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetIngressFrame(c.Request().Context())
		bodyBytes, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		downstreamBody = string(bodyBytes)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Nil(t, capturedFrame.RawBody)
	assert.True(t, capturedFrame.RawBodyTooLarge)
	assert.Equal(t, len(reqBody), len(downstreamBody))
	assert.True(t, strings.HasPrefix(downstreamBody, `{"model":"gpt-5-mini"`))
	assert.True(t, strings.HasSuffix(downstreamBody, `"}]}`))
}
