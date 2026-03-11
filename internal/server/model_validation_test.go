package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

type explodingValidationReadCloser struct{}

func (explodingValidationReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("live request body should not be read")
}

func (explodingValidationReadCloser) Close() error {
	return nil
}

func TestModelValidation(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini", "text-embedding-3-small"}}

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedBody   string
		handlerCalled  bool
	}{
		{
			name:           "valid model on chat completions",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid provider/model selector",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model with provider field",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"provider":"openai","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model on embeddings",
			method:         http.MethodPost,
			path:           "/v1/embeddings",
			body:           `{"model":"text-embedding-3-small","input":"hello"}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "valid model on responses",
			method:         http.MethodPost,
			path:           "/v1/responses",
			body:           `{"model":"gpt-4o-mini","input":"hello"}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "batch path skips root model validation",
			method:         http.MethodPost,
			path:           "/v1/batches",
			body:           `{"requests":[{"url":"/v1/chat/completions","body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}]}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "files path skips root model validation",
			method:         http.MethodPost,
			path:           "/v1/files",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "missing model returns 400",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "model is required",
			handlerCalled:  false,
		},
		{
			name:           "empty model returns 400",
			method:         http.MethodPost,
			path:           "/v1/embeddings",
			body:           `{"model":"","input":"hello"}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "model is required",
			handlerCalled:  false,
		},
		{
			name:           "unsupported model returns 400",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"model":"unsupported-model","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "unsupported model",
			handlerCalled:  false,
		},
		{
			name:           "provider field conflict returns 400",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{"provider":"anthropic","model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "conflicts",
			handlerCalled:  false,
		},
		{
			name:           "non-model path skips validation",
			method:         http.MethodGet,
			path:           "/v1/models",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "health path skips validation",
			method:         http.MethodGet,
			path:           "/health",
			body:           "",
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:           "invalid JSON passes through to handler",
			method:         http.MethodPost,
			path:           "/v1/chat/completions",
			body:           `{invalid}`,
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			handlerCalled := false

			middleware := ModelValidation(provider)
			handler := middleware(func(c *echo.Context) error {
				handlerCalled = true
				return c.String(http.StatusOK, "ok")
			})

			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			} else {
				body = strings.NewReader("")
			}

			req := httptest.NewRequest(tt.method, tt.path, body)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			assert.Equal(t, tt.handlerCalled, handlerCalled)

			if tt.expectedBody != "" {
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestModelValidation_SetsProviderType(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedProviderType string

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedProviderType = GetProviderType(c)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "mock", capturedProviderType)
}

func TestModelValidation_SetsRequestIDInContext(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedRequestID string

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedRequestID = core.GetRequestID(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "test-req-123", capturedRequestID)
}

func TestModelValidation_DoesNotTreatPrefixOvermatchAsBatchPath(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedRequestID string

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedRequestID = core.GetRequestID(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/batchesXYZ", strings.NewReader(`{"foo":"bar"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", capturedRequestID)
}

func TestModelValidation_BodyRewound(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var boundReq core.ChatRequest

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		if err := c.Bind(&boundReq); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o-mini", boundReq.Model)
	assert.Len(t, boundReq.Messages, 1)
}

func TestModelValidation_DoesNotReadLiveBodyWhenIngressFrameExistsWithoutOversizedBody(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	handlerCalled := false

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestModelValidation_ResolvesProviderTypeFromOversizedLiveBody(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes: map[string]string{
			"openai/gpt-4o-mini": "openai",
		},
	}

	e := echo.New()
	var capturedEnv *core.SemanticEnvelope
	var capturedProviderType string

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetSemanticEnvelope(c.Request().Context())
		capturedProviderType = GetProviderType(c)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"provider":"openai",
		"model":"gpt-4o-mini",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "oversized-live-body")

	frame := &core.IngressFrame{
		Method:          http.MethodPost,
		Path:            "/v1/chat/completions",
		ContentType:     "application/json",
		RawBodyTooLarge: true,
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	assert.Equal(t, "openai", capturedProviderType)
	canonicalReq := capturedEnv.CachedChatRequest()
	require.NotNil(t, canonicalReq)
	assert.Equal(t, "gpt-4o-mini", canonicalReq.Model)
	assert.Equal(t, "openai", canonicalReq.Provider)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestModelValidation_CachesCanonicalChatRequestFromIngressBody(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedEnv *core.SemanticEnvelope

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetSemanticEnvelope(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-4o-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hi"}],
			"response_format":{"type":"json_schema"}
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	canonicalReq := capturedEnv.CachedChatRequest()
	require.NotNil(t, canonicalReq)
	assert.Equal(t, "gpt-4o-mini", canonicalReq.Model)
	assert.Equal(t, "openai", canonicalReq.Provider)
	assert.NotNil(t, canonicalReq.ExtraFields["response_format"])
}

func TestModelValidation_CachesCanonicalResponsesRequestFromIngressBody(t *testing.T) {
	provider := &mockProvider{supportedModels: []string{"gpt-4o-mini"}}

	e := echo.New()
	var capturedEnv *core.SemanticEnvelope

	middleware := ModelValidation(provider)
	handler := middleware(func(c *echo.Context) error {
		capturedEnv = core.GetSemanticEnvelope(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingValidationReadCloser{}

	frame := &core.IngressFrame{
		Method:      http.MethodPost,
		Path:        "/v1/responses",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-4o-mini",
			"input":[{"type":"message","role":"user","content":"hi","x_trace":{"id":"trace-1"}}]
		}`),
	}
	ctx := core.WithIngressFrame(req.Context(), frame)
	ctx = core.WithSemanticEnvelope(ctx, core.BuildSemanticEnvelope(frame))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)
	require.NotNil(t, capturedEnv)
	canonicalReq := capturedEnv.CachedResponsesRequest()
	require.NotNil(t, canonicalReq)

	input, ok := canonicalReq.Input.([]core.ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1)
	assert.NotNil(t, input[0].ExtraFields["x_trace"])
}

func TestModelCtx_ReturnsContextAndProviderType(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(providerTypeKey), "openai")

	ctx, pt := ModelCtx(c)
	assert.NotNil(t, ctx)
	assert.Equal(t, "openai", pt)
}

func TestGetProviderType_EmptyWhenNotSet(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "", GetProviderType(c))
}
