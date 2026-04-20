package server

import (
	"bytes"
	"context"
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

	setPassthroughResolution(c, "openai", "openai", pp)
	setPassthroughRequestID(c, "req-123")

	svc := &passthroughService{
		responseHandler:   newRawPassthroughResponseHandler(),
		normalizeV1Prefix: false,
	}
	err := svc.ProviderPassthrough(c)
	require.NoError(t, err)

	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, `{"result":"ok"}`, rec.Body.String())
	assert.Equal(t, "req-123", rec.Header().Get("X-GoModel-Request-ID"))
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

func TestPassthroughRoute_RequestIDMiddleware_GeneratesUUID(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/p/openai/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedID string
	handler := GoModelRequestIDMiddleware()(func(c *echo.Context) error {
		capturedID = getPassthroughRequestID(c)
		return c.String(200, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	assert.NotEmpty(t, capturedID)
	assert.Equal(t, capturedID, rec.Header().Get("X-GoModel-Request-ID"))
}

func TestPassthroughRoute_RequestIDMiddleware_EchoesClientID(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/p/openai/v1/models", nil)
	req.Header.Set("X-GoModel-Request-ID", "client-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var capturedID string
	handler := GoModelRequestIDMiddleware()(func(c *echo.Context) error {
		capturedID = getPassthroughRequestID(c)
		return c.String(200, "ok")
	})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, "client-123", capturedID)
	assert.Equal(t, "client-123", rec.Header().Get("X-GoModel-Request-ID"))
}

func TestPassthroughRoute_HeaderFiltering_StripsSensitiveHeaders(t *testing.T) {
	req := &http.Request{
		Header: http.Header{
			"Authorization":       {"Bearer token"},
			"X-Request-ID":        {"internal-id"},
			"X-Api-Key":           {"api-key"},
			"Content-Type":        {"application/json"},
			"User-Agent":          {"test-client"},
			"X-Forwarded-For":     {"1.2.3.4"},
		},
	}

	headers := buildPassthroughHeaders(context.Background(), req.Header, "req-123")

	// Sensitive headers should be stripped
	assert.Empty(t, headers.Get("Authorization"))
	assert.Empty(t, headers.Get("X-Request-ID"))
	assert.Empty(t, headers.Get("X-Api-Key"))
	assert.Empty(t, headers.Get("X-Forwarded-For"))

	// Safe headers should be copied
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "test-client", headers.Get("User-Agent"))

	// GoModel request ID should be injected
	assert.Equal(t, "req-123", headers.Get("X-GoModel-Request-ID"))
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
