package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// imageMockProvider extends mockProvider (a RoutableProvider) with image
// generation support so the service layer can be exercised without a router.
type imageMockProvider struct {
	*mockProvider
	imageResp *core.ImageGenerationResponse
	imageErr  error
	resolved  *core.ModelSelector
	captured  *core.ImageGenerationRequest
}

func (m *imageMockProvider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if m.resolved != nil {
		return *m.resolved, true, nil
	}
	selector, err := core.ParseModelSelector(requested.Model, requested.ProviderHint)
	return selector, false, err
}

func (m *imageMockProvider) CreateImage(_ context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	m.captured = req
	if m.imageErr != nil {
		return nil, m.imageErr
	}
	return m.imageResp, nil
}

func newImageMock() *imageMockProvider {
	return &imageMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"dall-e-3"}},
		imageResp: &core.ImageGenerationResponse{
			Created: 1713833628,
			Data:    []core.ImageData{{URL: "https://img/1.png", RevisedPrompt: "a fluffy cat"}},
		},
	}
}

func TestImageGenerations_ReturnsProviderResponse(t *testing.T) {
	mock := newImageMock()
	svc := &imageService{provider: mock}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat","n":1,"size":"1024x1024","style":"vivid"}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ct := rec.Header().Get("Content-Type")
	assert.True(t, strings.HasPrefix(ct, "application/json"), "Content-Type = %q, want application/json", ct)

	got := echotest.Decode[map[string]any](t, rec)
	assert.Equal(t, float64(1713833628), got["created"])

	data, _ := got["data"].([]any)
	require.Len(t, data, 1)
	image, _ := data[0].(map[string]any)
	assert.Equal(t, "https://img/1.png", image["url"], "data[0] = %v, want url https://img/1.png", data[0])
	require.NotNil(t, mock.captured)
	assert.Equal(t, "dall-e-3", mock.captured.Model)
	assert.Empty(t, mock.captured.Provider)

	forwarded, _ := json.Marshal(mock.captured)
	assert.Contains(t, string(forwarded), `"style":"vivid"`, "extra field style not preserved in forwarded request: %s", forwarded)
}

func TestImageGenerations_RejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"malformed json", `{"model":`, "invalid request body"},
		{"missing prompt", `{"model":"dall-e-3"}`, "prompt is required"},
		{"zero n", `{"model":"dall-e-3","prompt":"a cat","n":0}`, "n must be at least 1"},
		{"streaming", `{"model":"gpt-image-1","prompt":"a cat","stream":true}`, "streaming image generation is not supported"},
		{"missing model", `{"prompt":"a cat"}`, "model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newImageMock()
			svc := &imageService{provider: mock}
			c, rec := echotest.Post(t, "/v1/images/generations", tt.body)
			err := svc.CreateImage(c)
			require.NoError(t, err)
			require.NotEqual(t, http.StatusOK, rec.Code, "status = 200, want an error (body: %s)", rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantMsg)
			assert.Nil(t, mock.captured)
		})
	}
}

func TestImageGenerations_RouterWithoutImageSupport(t *testing.T) {
	svc := &imageService{provider: &mockProvider{supportedModels: []string{"dall-e-3"}}}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "image generation is not supported")
}

// TestImageGenerations_AuthorizesResolvedSelector verifies the authorizer
// receives the registry-resolved (provider-qualified) selector and that a
// denial stops the call before the provider is reached.
func TestImageGenerations_AuthorizesResolvedSelector(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		mock := newImageMock()
		mock.resolved = &core.ModelSelector{Provider: "openai", Model: "dall-e-3"}
		authorizer := &recordingModelAuthorizer{}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
		err := svc.CreateImage(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "openai", authorizer.lastSelector.Provider)
		assert.Equal(t, "dall-e-3", authorizer.lastSelector.Model, "authorizer saw %+v, want resolved openai/dall-e-3", authorizer.lastSelector)
	})

	t.Run("denied", func(t *testing.T) {
		mock := newImageMock()
		authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
		err := svc.CreateImage(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Nil(t, mock.captured)
	})
}

func TestImageGenerations_ProviderErrorIsSurfaced(t *testing.T) {
	mock := newImageMock()
	mock.imageErr = core.NewProviderError("openai", http.StatusBadRequest, "Your request was rejected by the safety system.", nil)
	svc := &imageService{provider: mock}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "safety system")
}

func TestImageGenerations_NilProviderResponseIs502(t *testing.T) {
	mock := newImageMock()
	mock.imageResp = nil
	mock.providerNames = map[string]string{"dall-e-3": "image-primary"}
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &imageService{provider: mock, usageLogger: logger}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"provider":"image-primary"`)
	assert.Contains(t, rec.Body.String(), "provider image-primary returned empty image response")
	assert.Nil(t, captured)
}

func TestImageGenerations_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	mock := newImageMock()
	mock.imageResp.Data = append(mock.imageResp.Data, core.ImageData{URL: "https://img/2.png"})
	pricing := &core.ModelPricing{PerImage: new(0.04)}
	svc := &imageService{
		provider:        mock,
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: pricing}}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat","n":2}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.Equal(t, "/v1/images/generations", captured.Endpoint)
	assert.Equal(t, "dall-e-3", captured.Model)
	got := captured.RawData["images"]
	assert.Equal(t, 2, got)
	require.NotNil(t, captured.TotalCost)
	assert.GreaterOrEqual(t, *captured.TotalCost, 0.0799)
	assert.LessOrEqual(t, *captured.TotalCost, 0.0801)
}

func TestImageGenerations_UsageDisabledWritesNothing(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: false}, captured: &captured}
	svc := &imageService{provider: newImageMock(), usageLogger: logger}
	c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
	err := svc.CreateImage(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, captured)
}

// TestImageGenerations_HandlerRoute verifies POST /v1/images/generations is
// registered on the HTTP server and reaches the image service end to end.
func TestImageGenerations_HandlerRoute(t *testing.T) {
	mock := newImageMock()
	srv := New(mock, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3","prompt":"a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := echotest.Decode[map[string]any](t, rec)

	data, _ := got["data"].([]any)
	require.Len(t, data, 1)
	image, _ := data[0].(map[string]any)
	assert.Equal(t, "https://img/1.png", image["url"], "data[0] = %v, want url https://img/1.png", data[0])
	require.NotNil(t, mock.captured)
	assert.Equal(t, "a cat", mock.captured.Prompt)
}

// TestImageGenerations_AuditsRequestBody verifies the prompt reaches the audit
// entry when body logging is on: the endpoint is not ingress-managed, so the
// middleware has no request snapshot and relies on the service to capture it.
func TestImageGenerations_AuditsRequestBody(t *testing.T) {
	for _, logBodies := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[logBodies], func(t *testing.T) {
			mock := newImageMock()
			mock.resolved = &core.ModelSelector{Provider: "openai", Model: "dall-e-3"}
			svc := &imageService{provider: mock, logBodies: logBodies}
			c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat","style":"vivid"}`)
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)
			err := svc.CreateImage(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			// mockProvider reports "mock" as the provider type for every model.
			assert.Equal(t, "dall-e-3", entry.RequestedModel)
			assert.Equal(t, "openai/dall-e-3", entry.ResolvedModel)
			assert.Equal(t, "mock", entry.Provider)

			if !logBodies {
				if entry.Data != nil {
					require.Nil(t, entry.Data.RequestBody, "body logging is off")
				}
				return
			}
			body, ok := auditlog.BodyDocument(entry.Data.RequestBody).(map[string]any)
			require.True(t, ok, "request body = %T, want JSON object", entry.Data.RequestBody)
			assert.Equal(t, "a cat", body["prompt"])
			assert.Equal(t, "vivid", body["style"], "audited request body = %v, want prompt and extra fields", body)
		})
	}
}

// TestImageGenerations_AuditsResponseImages verifies the response is recorded
// as an image body (envelope metadata plus per-image items) and that base64
// output is embedded only when output logging is enabled, so a large b64_json
// payload never trips the generic capture limit.
func TestImageGenerations_AuditsResponseImages(t *testing.T) {
	tests := []struct {
		name            string
		logBodies       bool
		logImageOutputs bool
	}{
		{name: "bodies off", logBodies: false},
		{name: "metadata only", logBodies: true},
		{name: "outputs stored", logBodies: true, logImageOutputs: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newImageMock()
			mock.imageResp = &core.ImageGenerationResponse{
				Created: 1713833628,
				Size:    "1024x1024",
				Data:    []core.ImageData{{B64JSON: "aGVsbG8="}, {URL: "https://img/2.png"}},
				Usage:   &core.ImageUsage{TotalTokens: 300},
			}
			svc := &imageService{provider: mock, logBodies: tt.logBodies, logImageOutputs: tt.logImageOutputs}
			c, rec := echotest.Post(t, "/v1/images/generations", `{"model":"dall-e-3","prompt":"a cat"}`)
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)
			err := svc.CreateImage(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			if !tt.logBodies {
				if entry.Data != nil {
					require.Nil(t, entry.Data.ResponseBody, "body logging is off")
				}
				return
			}
			assert.True(t, auditlog.IsResponseBodyCapturedByHandler(c))

			body, ok := entry.Data.ResponseBody.(auditlog.ImageBodyLog)
			require.True(t, ok, "response body = %T, want auditlog.ImageBodyLog", entry.Data.ResponseBody)
			assert.Equal(t, "1024x1024", body.Meta["size"])
			assert.Equal(t, int64(1713833628), body.Meta["created"], "response meta = %v", body.Meta)
			require.Len(t, body.Items, 2)

			b64, hosted := body.Items[0], body.Items[1]
			assert.Equal(t, 5, b64.Bytes)
			assert.Equal(t, tt.logImageOutputs, b64.Stored)
			if tt.logImageOutputs {
				assert.Equal(t, "aGVsbG8=", b64.Data)
			}
			assert.Equal(t, "https://img/2.png", hosted.URL)
		})
	}
}
