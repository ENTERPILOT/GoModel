package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
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

func TestRequestSnapshotCapture_SetsSnapshotAndSemantics(t *testing.T) {
	e := echo.New()

	reqBody := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?foo=bar", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.RequestSnapshot
	var capturedEnv *core.WhiteBoxPrompt
	var downstreamBody string

	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
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
	assert.Equal(t, "", capturedFrame.UserPath)
	assert.Equal(t, []string{"bar"}, capturedFrame.GetQueryParams()["foo"])
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", capturedFrame.GetTraceMetadata()["Traceparent"])
	assert.JSONEq(t, reqBody, string(capturedFrame.CapturedBody()))
	assert.JSONEq(t, reqBody, downstreamBody)

	require.NotNil(t, capturedEnv)
	assert.Equal(t, "openai_compat", capturedEnv.RouteType)
	assert.Equal(t, "chat_completions", capturedEnv.OperationType)
	assert.Equal(t, "gpt-5-mini", capturedEnv.RouteHints.Model)
	assert.True(t, capturedEnv.JSONBodyParsed)
	assert.Nil(t, capturedEnv.CachedChatRequest())
	assert.Nil(t, capturedEnv.CachedResponsesRequest())
	assert.Nil(t, capturedEnv.CachedEmbeddingRequest())
	assert.Nil(t, capturedEnv.CachedBatchRequest())
}

func TestRequestSnapshotCapture_NormalizesUserPathHeader(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(core.UserPathHeader, " team//alpha/user/ ")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.RequestSnapshot
	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedFrame)
	assert.Equal(t, "/team/alpha/user", capturedFrame.UserPath)
	assert.Equal(t, "/team/alpha/user", c.Request().Header.Get(core.UserPathHeader))
}

func TestRequestSnapshotCapture_PreservesPassthroughRouteParams(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "provider", Value: "openai"},
		{Name: "endpoint", Value: "responses"},
	})

	var capturedFrame *core.RequestSnapshot
	var capturedEnv *core.WhiteBoxPrompt

	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Equal(t, "openai", capturedFrame.GetRouteParams()["provider"])
	assert.Equal(t, "responses", capturedFrame.GetRouteParams()["endpoint"])

	require.NotNil(t, capturedEnv)
	assert.Equal(t, "provider_passthrough", capturedEnv.RouteType)
	assert.Equal(t, "openai", capturedEnv.RouteHints.Provider)
	assert.Equal(t, "responses", capturedEnv.RouteHints.Endpoint)
	if info := capturedEnv.CachedPassthroughRouteInfo(); assert.NotNil(t, info) {
		assert.Equal(t, "openai", info.Provider)
		assert.Equal(t, "responses", info.RawEndpoint)
		assert.Equal(t, "/p/openai/responses", info.AuditPath)
	}
}

func TestRequestSnapshotCapture_GeneratesRequestIDWhenMissing(t *testing.T) {
	e := echo.New()

	reqBody := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.RequestSnapshot
	var downstreamBody string

	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		bodyBytes, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		downstreamBody = string(bodyBytes)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	if capturedFrame.RequestID == "" {
		t.Fatal("expected generated request id")
	}
	if _, parseErr := uuid.Parse(capturedFrame.RequestID); parseErr != nil {
		t.Fatalf("generated request id is not a valid UUID: %v", parseErr)
	}
	if got := rec.Result().Header.Get("X-Request-ID"); got != capturedFrame.RequestID {
		t.Fatalf("response X-Request-ID = %q, want %q", got, capturedFrame.RequestID)
	}
	if got := core.GetRequestID(c.Request().Context()); got != capturedFrame.RequestID {
		t.Fatalf("context request id = %q, want %q", got, capturedFrame.RequestID)
	}
	assert.JSONEq(t, reqBody, downstreamBody)
}

func TestExtractTraceMetadata_JoinsMultipleHeaderValues(t *testing.T) {
	metadata := extractTraceMetadata(http.Header{
		"Traceparent": {"00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"},
		"Tracestate":  {"vendor=a", "vendor=b"},
		"Baggage":     {"foo=1", "bar=2"},
	})

	assert.Equal(t, "vendor=a,vendor=b", metadata["Tracestate"])
	assert.Equal(t, "foo=1,bar=2", metadata["Baggage"])
}

func TestModelValidation_UsesSemanticEnvelopeWithoutReadingBody(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-123")
	req.Body = &explodingReadCloser{}

	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
		false,
		"req-123",
		nil,
	)
	ctx := core.WithRequestSnapshot(req.Context(), frame)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := WorkflowResolution(provider)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequestSnapshotCapture_SkipsOversizedBodies(t *testing.T) {
	e := echo.New()

	largeContent := strings.Repeat("x", int(auditlog.MaxBodyCapture)+128)
	reqBody := `{"model":"gpt-5-mini","messages":[{"role":"user","content":"` + largeContent + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.RequestSnapshot
	var downstreamBody string

	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		bodyBytes, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		downstreamBody = string(bodyBytes)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Nil(t, capturedFrame.CapturedBody())
	assert.True(t, capturedFrame.BodyNotCaptured)
	assert.Equal(t, len(reqBody), len(downstreamBody))
	assert.True(t, strings.HasPrefix(downstreamBody, `{"model":"gpt-5-mini"`))
	assert.True(t, strings.HasSuffix(downstreamBody, `"}]}`))
}

func TestRequestSnapshotCapture_ManagesFilesWithoutReadingMultipartBody(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	req.Body = &explodingReadCloser{}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedFrame *core.RequestSnapshot
	var capturedEnv *core.WhiteBoxPrompt

	handler := RequestSnapshotCapture()(func(c *echo.Context) error {
		capturedFrame = core.GetRequestSnapshot(c.Request().Context())
		capturedEnv = core.GetWhiteBoxPrompt(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)

	require.NotNil(t, capturedFrame)
	assert.Equal(t, "/v1/files", capturedFrame.Path)
	assert.Equal(t, "multipart/form-data; boundary=test", capturedFrame.ContentType)
	assert.Nil(t, capturedFrame.CapturedBody())
	assert.False(t, capturedFrame.BodyNotCaptured)

	require.NotNil(t, capturedEnv)
	assert.Equal(t, "files", capturedEnv.OperationType)
	assert.False(t, capturedEnv.JSONBodyParsed)
}

func TestRequestBodyBytes_UsesSnapshotReadOnlyBodyView(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = &explodingReadCloser{}
	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"gpt-4o-mini"}`),
		false,
		"req-body-bytes-123",
		nil,
	)
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), frame))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	body, err := requestBodyBytes(c)
	require.NoError(t, err)
	require.NotNil(t, body)
	assert.JSONEq(t, `{"model":"gpt-4o-mini"}`, string(body))

	view := frame.CapturedBodyView()
	require.NotNil(t, view)
	require.NotEmpty(t, view)
	if &body[0] != &view[0] {
		t.Fatal("requestBodyBytes returned a cloned snapshot body, want shared read-only view")
	}
}
