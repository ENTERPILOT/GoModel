package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestPassthroughRoute_ProviderPassthrough_ForwardsResponse(t *testing.T) {
	pp := &mockDirectPassthroughProvider{
		inner: &mockProvider{
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: 200,
				Headers:    map[string][]string{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"result":"ok"}`))),
			},
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4"}`)))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	req.Header.Set(core.RequestIDHeader, "req-123")
	req, _ = ensureRequestID(req)
	c.SetRequest(req)
	setPassthroughResolution(c, "openai", pp)
	assert.Equal(t, "openai", getPassthroughProviderType(c))
	assert.Equal(t, pp, getPassthroughProvider(c))

	svc := &passthroughService{
		responseHandler:   newRawPassthroughResponseHandler(),
		normalizeV1Prefix: false,
	}
	err := svc.ProviderPassthrough(c)
	require.NoError(t, err)

	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, `{"result":"ok"}`, rec.Body.String())
	assert.Equal(t, "req-123", rec.Header().Get(core.RequestIDHeader))
}

func TestPassthroughRoute_GuardrailsMiddleware_RestoresBody(t *testing.T) {
	e := echo.New()
	bodyContent := `{"model":"gpt-4","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/chat/completions", bytes.NewReader([]byte(bodyContent)))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Patcher that reads the body
	readingPatcher := &mockPatcher{fn: func(ctx context.Context, cr *core.ChatRequest) (*core.ChatRequest, error) {
		return cr, nil
	}}

	var bodyReadByHandler []byte
	handler := PassthroughGuardrailsMiddleware(readingPatcher)(func(c *echo.Context) error {
		var err error
		bodyReadByHandler, err = io.ReadAll(c.Request().Body)
		return err
	})

	err := handler(c)
	require.NoError(t, err)
	// Body should be readable again in the handler
	assert.Equal(t, bodyContent, string(bodyReadByHandler))
}

func TestPassthroughRoute_ProviderResolutionMiddleware_RequestBodyReadError(t *testing.T) {
	provider := &mockProvider{}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai/responses", nil)
	req.Body = io.NopCloser(failingReader{err: errors.New("read failed")})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var reachedHandler bool
	handler := PassthroughProviderResolutionMiddleware(provider, nil)(func(c *echo.Context) error {
		reachedHandler = true
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	assert.False(t, reachedHandler)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var env core.OpenAIErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, core.ErrorTypeInvalidRequest, env.Error.Type)
	assert.Contains(t, env.Error.Message, "failed to read request body")
}

type failingReader struct {
	err error
}

func (r failingReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func TestPassthroughRoute_ProviderResolutionMiddleware_RejectsDisabledInstance(t *testing.T) {
	provider := &mockProvider{}
	disabledInstances := map[string]struct{}{"disabled-provider": {}}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/p/disabled-provider/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var reachedHandler bool
	handler := PassthroughProviderResolutionMiddleware(provider, disabledInstances)(func(c *echo.Context) error {
		reachedHandler = true
		return c.String(200, "should not reach")
	})

	_ = handler(c)
	// Handler should not be reached due to disabled instance
	assert.False(t, reachedHandler)
	// Response should indicate error
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPassthroughRoute_RequestID_GeneratesUUID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/p/openai/v1/models", nil)
	req, id := ensureRequestID(req)
	assert.NotEmpty(t, id)
	assert.Equal(t, id, req.Header.Get(core.RequestIDHeader))
}

func TestPassthroughRoute_RequestID_EchoesClientID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/p/openai/v1/models", nil)
	req.Header.Set(core.RequestIDHeader, "client-123")
	req, id := ensureRequestID(req)
	assert.Equal(t, "client-123", id)
	assert.Equal(t, "client-123", req.Header.Get(core.RequestIDHeader))
}

func TestPassthroughRoute_HeaderFiltering_StripsSensitiveHeaders(t *testing.T) {
	req := &http.Request{
		Header: http.Header{
			"Authorization":       {"Bearer token"},
			core.RequestIDHeader:        {"internal-id"},
			"X-Api-Key":           {"api-key"},
			"Content-Type":        {"application/json"},
			"User-Agent":          {"test-client"},
			"X-Forwarded-For":     {"1.2.3.4"},
		},
	}

	headers := buildPassthroughHeaders(context.Background(), req.Header, "req-123")

	// Sensitive headers should be stripped
	assert.Empty(t, headers.Get("Authorization"))
	assert.Empty(t, headers.Get("X-Api-Key"))
	assert.Empty(t, headers.Get("X-Forwarded-For"))

	// Safe headers should be copied
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "test-client", headers.Get("User-Agent"))

	// Client-supplied request ID is replaced by the gateway-generated one
	assert.Equal(t, "req-123", headers.Get(core.RequestIDHeader))
}

type mockPatcher struct {
	err error
	fn  func(context.Context, *core.ChatRequest) (*core.ChatRequest, error)
}

func (m *mockPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	if m.fn != nil {
		return m.fn(ctx, req)
	}
	return req, m.err
}

func (m *mockPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return req, m.err
}
