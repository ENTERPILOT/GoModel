package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
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

func newImageRequest(body string) (*echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	return echo.New().NewContext(req, rec), rec
}

func TestImageGenerations_ReturnsProviderResponse(t *testing.T) {
	mock := newImageMock()
	svc := &imageService{modelCallService: modelCallService{provider: mock}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat","n":1,"size":"1024x1024","style":"vivid"}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (body: %s)", err, rec.Body.String())
	}
	if got["created"] != float64(1713833628) {
		t.Errorf("created = %v, want 1713833628", got["created"])
	}
	data, _ := got["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %v, want one image", got["data"])
	}
	if image, _ := data[0].(map[string]any); image["url"] != "https://img/1.png" {
		t.Errorf("data[0] = %v, want url https://img/1.png", data[0])
	}

	if mock.captured == nil {
		t.Fatal("provider was not called")
	}
	if mock.captured.Model != "dall-e-3" || mock.captured.Provider != "" {
		t.Errorf("provider saw %q/%q, want dall-e-3 with provider hint stripped", mock.captured.Provider, mock.captured.Model)
	}
	forwarded, _ := json.Marshal(mock.captured)
	if !strings.Contains(string(forwarded), `"style":"vivid"`) {
		t.Errorf("extra field style not preserved in forwarded request: %s", forwarded)
	}
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
			svc := &imageService{modelCallService: modelCallService{provider: mock}}
			c, rec := newImageRequest(tt.body)

			if err := svc.CreateImage(c); err != nil {
				t.Fatalf("CreateImage returned error: %v", err)
			}
			if rec.Code == http.StatusOK {
				t.Fatalf("status = 200, want an error (body: %s)", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantMsg) {
				t.Errorf("body = %s, want %q", rec.Body.String(), tt.wantMsg)
			}
			if mock.captured != nil {
				t.Error("provider should not be called for an invalid request")
			}
		})
	}
}

func TestImageGenerations_RouterWithoutImageSupport(t *testing.T) {
	svc := &imageService{modelCallService: modelCallService{provider: &mockProvider{supportedModels: []string{"dall-e-3"}}}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "image generation is not supported") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// TestImageGenerations_AuthorizesResolvedSelector verifies the authorizer
// receives the registry-resolved (provider-qualified) selector and that a
// denial stops the call before the provider is reached.
func TestImageGenerations_AuthorizesResolvedSelector(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		mock := newImageMock()
		mock.resolved = &core.ModelSelector{Provider: "openai", Model: "dall-e-3"}
		authorizer := &recordingModelAuthorizer{}
		svc := &imageService{modelCallService: modelCallService{provider: mock, modelAuthorizer: authorizer}}
		c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

		if err := svc.CreateImage(c); err != nil {
			t.Fatalf("CreateImage returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if authorizer.lastSelector.Provider != "openai" || authorizer.lastSelector.Model != "dall-e-3" {
			t.Errorf("authorizer saw %+v, want resolved openai/dall-e-3", authorizer.lastSelector)
		}
	})

	t.Run("denied", func(t *testing.T) {
		mock := newImageMock()
		authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
		svc := &imageService{modelCallService: modelCallService{provider: mock, modelAuthorizer: authorizer}}
		c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

		if err := svc.CreateImage(c); err != nil {
			t.Fatalf("CreateImage returned error: %v", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if mock.captured != nil {
			t.Error("provider should not be called when access is denied")
		}
	})
}

func TestImageGenerations_ProviderErrorIsSurfaced(t *testing.T) {
	mock := newImageMock()
	mock.imageErr = core.NewProviderError("openai", http.StatusBadRequest, "Your request was rejected by the safety system.", nil)
	svc := &imageService{modelCallService: modelCallService{provider: mock}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "safety system") {
		t.Errorf("body = %s, want upstream message", rec.Body.String())
	}
}

func TestImageGenerations_NilProviderResponseIs502(t *testing.T) {
	mock := newImageMock()
	mock.imageResp = nil
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &imageService{modelCallService: modelCallService{provider: mock, usageLogger: logger}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured != nil {
		t.Error("no usage entry should be written for a failed call")
	}
}

func TestImageGenerations_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	mock := newImageMock()
	mock.imageResp.Data = append(mock.imageResp.Data, core.ImageData{URL: "https://img/2.png"})
	pricing := &core.ModelPricing{PerImage: new(0.04)}
	svc := &imageService{modelCallService: modelCallService{
		provider:        mock,
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: pricing},
	}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat","n":2}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("expected a usage entry to be written")
	}
	if captured.Endpoint != "/v1/images/generations" {
		t.Errorf("endpoint = %q, want /v1/images/generations", captured.Endpoint)
	}
	if captured.Model != "dall-e-3" {
		t.Errorf("model = %q, want dall-e-3", captured.Model)
	}
	if got := captured.RawData["images"]; got != 2 {
		t.Errorf("images = %v, want 2", got)
	}
	if captured.TotalCost == nil || *captured.TotalCost < 0.0799 || *captured.TotalCost > 0.0801 {
		t.Errorf("total cost = %v, want 0.08", captured.TotalCost)
	}
}

func TestImageGenerations_UsageDisabledWritesNothing(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: false}, captured: &captured}
	svc := &imageService{modelCallService: modelCallService{provider: newImageMock(), usageLogger: logger}}
	c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)

	if err := svc.CreateImage(c); err != nil {
		t.Fatalf("CreateImage returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if captured != nil {
		t.Error("usage entry written although tracking is disabled")
	}
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

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (body: %s)", err, rec.Body.String())
	}
	data, _ := got["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %v, want one image", got["data"])
	}
	if image, _ := data[0].(map[string]any); image["url"] != "https://img/1.png" {
		t.Errorf("data[0] = %v, want url https://img/1.png", data[0])
	}
	if mock.captured == nil || mock.captured.Prompt != "a cat" {
		t.Errorf("provider did not receive the routed request: %+v", mock.captured)
	}
}

// TestImageGenerations_AuditsRequestBody verifies the prompt reaches the audit
// entry when body logging is on: the endpoint is not ingress-managed, so the
// middleware has no request snapshot and relies on the service to capture it.
func TestImageGenerations_AuditsRequestBody(t *testing.T) {
	for _, logBodies := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[logBodies], func(t *testing.T) {
			mock := newImageMock()
			mock.resolved = &core.ModelSelector{Provider: "openai", Model: "dall-e-3"}
			svc := &imageService{modelCallService: modelCallService{provider: mock}, logBodies: logBodies}
			c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat","style":"vivid"}`)
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)

			if err := svc.CreateImage(c); err != nil {
				t.Fatalf("CreateImage returned error: %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			// mockProvider reports "mock" as the provider type for every model.
			if entry.RequestedModel != "dall-e-3" || entry.ResolvedModel != "openai/dall-e-3" || entry.Provider != "mock" {
				t.Errorf("audit route = requested %q resolved %q provider %q, want dall-e-3 / openai/dall-e-3 / mock", entry.RequestedModel, entry.ResolvedModel, entry.Provider)
			}
			if !logBodies {
				if entry.Data != nil && entry.Data.RequestBody != nil {
					t.Fatalf("request body captured although body logging is off: %v", entry.Data.RequestBody)
				}
				return
			}
			body, ok := entry.Data.RequestBody.(map[string]any)
			if !ok {
				t.Fatalf("request body = %T, want decoded JSON object", entry.Data.RequestBody)
			}
			if body["prompt"] != "a cat" || body["style"] != "vivid" {
				t.Errorf("audited request body = %v, want prompt and extra fields", body)
			}
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
			svc := &imageService{modelCallService: modelCallService{provider: mock}, logBodies: tt.logBodies, logImageOutputs: tt.logImageOutputs}
			c, rec := newImageRequest(`{"model":"dall-e-3","prompt":"a cat"}`)
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)

			if err := svc.CreateImage(c); err != nil {
				t.Fatalf("CreateImage returned error: %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if !tt.logBodies {
				if entry.Data != nil && entry.Data.ResponseBody != nil {
					t.Fatalf("response body captured although body logging is off: %v", entry.Data.ResponseBody)
				}
				return
			}
			if !auditlog.IsResponseBodyCapturedByHandler(c) {
				t.Error("handler must mark the response body as captured so the middleware leaves it alone")
			}
			body, ok := entry.Data.ResponseBody.(auditlog.ImageBodyLog)
			if !ok {
				t.Fatalf("response body = %T, want auditlog.ImageBodyLog", entry.Data.ResponseBody)
			}
			if body.Meta["size"] != "1024x1024" || body.Meta["created"] != int64(1713833628) {
				t.Errorf("response meta = %v", body.Meta)
			}
			if len(body.Items) != 2 {
				t.Fatalf("items = %+v, want base64 and url outputs", body.Items)
			}
			b64, hosted := body.Items[0], body.Items[1]
			if b64.Bytes != 5 || b64.Stored != tt.logImageOutputs || (tt.logImageOutputs && b64.Data != "aGVsbG8=") {
				t.Errorf("base64 item = %+v, want stored=%v", b64, tt.logImageOutputs)
			}
			if hosted.URL != "https://img/2.png" {
				t.Errorf("url item = %+v", hosted)
			}
		})
	}
}
