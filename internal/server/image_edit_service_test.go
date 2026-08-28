package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// imageEditMockProvider adds edit support on top of imageMockProvider.
type imageEditMockProvider struct {
	*imageMockProvider
	capturedEdit *core.ImageEditRequest
}

func (m *imageEditMockProvider) CreateImageEdit(_ context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	m.capturedEdit = req
	if m.imageErr != nil {
		return nil, m.imageErr
	}
	return m.imageResp, nil
}

func newImageEditMock() *imageEditMockProvider {
	mock := newImageMock()
	mock.supportedModels = []string{"gpt-image-1", "dall-e-2"}
	mock.imageResp = &core.ImageGenerationResponse{
		Created: 1713833628,
		Data:    []core.ImageData{{B64JSON: "aGk="}},
		Usage:   &core.ImageUsage{InputTokens: 50, OutputTokens: 1000, TotalTokens: 1050},
	}
	return &imageEditMockProvider{imageMockProvider: mock}
}

type editFormFile struct {
	field, filename, contentType, data string
}

// editForm builds a multipart/form-data body. Values with the same name may be
// repeated by listing them more than once.
func editForm(t *testing.T, values [][2]string, files ...editFormFile) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, kv := range values {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="`+f.field+`"; filename="`+f.filename+`"`)
		if f.contentType != "" {
			h.Set("Content-Type", f.contentType)
		}
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		_, _ = part.Write([]byte(f.data))
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func newImageEditRequest(t *testing.T, values [][2]string, files ...editFormFile) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body, contentType := editForm(t, values, files...)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	return echo.New().NewContext(req, rec), rec
}

var catPNG = editFormFile{field: "image", filename: "cat.png", contentType: "image/png", data: "cat-bytes"}

func TestImageEdits_ReturnsProviderResponse(t *testing.T) {
	mock := newImageEditMock()
	svc := &imageService{provider: mock}
	c, rec := newImageEditRequest(t,
		[][2]string{{"model", "gpt-image-1"}, {"prompt", "add a hat"}, {"n", "1"}, {"size", "1024x1024"}, {"input_fidelity", "high"}},
		catPNG,
		editFormFile{field: "mask", filename: "mask.png", contentType: "image/png", data: "mask-bytes"},
	)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
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
	if image, _ := data[0].(map[string]any); image["b64_json"] != "aGk=" {
		t.Errorf("data[0] = %v, want b64_json aGk=", data[0])
	}

	req := mock.capturedEdit
	if req == nil {
		t.Fatal("provider was not called")
	}
	if req.Model != "gpt-image-1" || req.Provider != "" || req.Prompt != "add a hat" {
		t.Errorf("provider saw %+v", req)
	}
	if len(req.Images) != 1 || req.Images[0].Filename != "cat.png" || req.Images[0].ContentType != "image/png" || string(req.Images[0].Data) != "cat-bytes" {
		t.Errorf("images = %+v", req.Images)
	}
	if req.Mask == nil || string(req.Mask.Data) != "mask-bytes" {
		t.Errorf("mask = %+v", req.Mask)
	}
	want := []core.FormField{{Name: "input_fidelity", Value: "high"}, {Name: "n", Value: "1"}, {Name: "size", Value: "1024x1024"}}
	if len(req.Fields) != len(want) {
		t.Fatalf("fields = %+v, want %+v", req.Fields, want)
	}
	for i := range want {
		if req.Fields[i] != want[i] {
			t.Errorf("fields[%d] = %+v, want %+v", i, req.Fields[i], want[i])
		}
	}
}

func TestImageEdits_CollectsImageArray(t *testing.T) {
	mock := newImageEditMock()
	svc := &imageService{provider: mock}
	c, rec := newImageEditRequest(t,
		[][2]string{{"model", "gpt-image-1"}, {"prompt", "combine"}},
		editFormFile{field: "image[]", filename: "one.png", data: "one"},
		editFormFile{field: "image[]", filename: "two.png", data: "two"},
	)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if req := mock.capturedEdit; req == nil || len(req.Images) != 2 || string(req.Images[0].Data) != "one" || string(req.Images[1].Data) != "two" {
		t.Errorf("images = %+v", mock.capturedEdit)
	}
}

func TestImageEdits_RejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		values  [][2]string
		files   []editFormFile
		wantMsg string
	}{
		{"missing model", [][2]string{{"prompt", "x"}}, []editFormFile{catPNG}, "model is required"},
		{"missing prompt", [][2]string{{"model", "gpt-image-1"}}, []editFormFile{catPNG}, "prompt is required"},
		{"missing image", [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, nil, "image is required"},
		{"zero n", [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}, {"n", "0"}}, []editFormFile{catPNG}, "n must be at least 1"},
		{"streaming", [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}, {"stream", "true"}}, []editFormFile{catPNG}, "streaming image edits are not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newImageEditMock()
			svc := &imageService{provider: mock}
			c, rec := newImageEditRequest(t, tt.values, tt.files...)

			if err := svc.CreateImageEdit(c); err != nil {
				t.Fatalf("CreateImageEdit returned error: %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantMsg) {
				t.Errorf("body = %s, want %q", rec.Body.String(), tt.wantMsg)
			}
			if mock.capturedEdit != nil {
				t.Error("provider should not be called for an invalid request")
			}
		})
	}
}

func TestImageEdits_RejectsNonMultipartBody(t *testing.T) {
	svc := &imageService{provider: newImageEditMock()}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"gpt-image-1","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if err := svc.CreateImageEdit(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid multipart form") {
		t.Fatalf("status = %d body = %s, want 400 invalid multipart form", rec.Code, rec.Body.String())
	}
}

func TestImageEdits_RouterWithoutEditSupport(t *testing.T) {
	// A router that generates images but cannot edit them.
	svc := &imageService{provider: newImageMock()}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "dall-e-3"}, {"prompt", "x"}}, catPNG)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "image edits are not supported") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestImageEdits_AuthorizesResolvedSelector(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		mock := newImageEditMock()
		mock.resolved = &core.ModelSelector{Provider: "openai", Model: "gpt-image-1"}
		authorizer := &recordingModelAuthorizer{}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)

		if err := svc.CreateImageEdit(c); err != nil {
			t.Fatalf("CreateImageEdit returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if authorizer.lastSelector.Provider != "openai" || authorizer.lastSelector.Model != "gpt-image-1" {
			t.Errorf("authorizer saw %+v, want resolved openai/gpt-image-1", authorizer.lastSelector)
		}
	})

	t.Run("denied", func(t *testing.T) {
		mock := newImageEditMock()
		authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)

		if err := svc.CreateImageEdit(c); err != nil {
			t.Fatalf("CreateImageEdit returned error: %v", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if mock.capturedEdit != nil {
			t.Error("provider should not be called when access is denied")
		}
	})
}

func TestImageEdits_ProviderErrorIsSurfaced(t *testing.T) {
	mock := newImageEditMock()
	mock.imageErr = core.NewProviderError("openai", http.StatusBadRequest, "Invalid image format", nil)
	svc := &imageService{provider: mock}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid image format") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestImageEdits_NilProviderResponseIs502(t *testing.T) {
	mock := newImageEditMock()
	mock.imageResp = nil
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &imageService{provider: mock, usageLogger: logger}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured != nil {
		t.Error("no usage entry should be written for a failed call")
	}
}

func TestImageEdits_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	mock := newImageEditMock()
	pricing := &core.ModelPricing{PerImage: new(0.04)}
	svc := &imageService{
		provider:        mock,
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: pricing}}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)

	if err := svc.CreateImageEdit(c); err != nil {
		t.Fatalf("CreateImageEdit returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("expected a usage entry to be written")
	}
	if captured.Endpoint != "/v1/images/edits" {
		t.Errorf("endpoint = %q, want /v1/images/edits", captured.Endpoint)
	}
	if captured.Model != "gpt-image-1" || captured.TotalTokens != 1050 {
		t.Errorf("entry = model %q tokens %d", captured.Model, captured.TotalTokens)
	}
	if got := captured.RawData["images"]; got != 1 {
		t.Errorf("images = %v, want 1", got)
	}
	if captured.TotalCost == nil || *captured.TotalCost < 0.0399 || *captured.TotalCost > 0.0401 {
		t.Errorf("total cost = %v, want 0.04", captured.TotalCost)
	}
}

// TestImageEdits_HandlerRoute verifies POST /v1/images/edits is registered on
// the HTTP server and reaches the image service end to end.
func TestImageEdits_HandlerRoute(t *testing.T) {
	mock := newImageEditMock()
	srv := New(mock, nil)

	body, contentType := editForm(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "add a hat"}}, catPNG)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (body: %s)", err, rec.Body.String())
	}
	if data, _ := got["data"].([]any); len(data) != 1 {
		t.Fatalf("data = %v, want one image", got["data"])
	}
	if mock.capturedEdit == nil || mock.capturedEdit.Prompt != "add a hat" || len(mock.capturedEdit.Images) != 1 {
		t.Errorf("provider did not receive the routed request: %+v", mock.capturedEdit)
	}
}

// TestImageEdits_AuditsRequestMetadata verifies the edit parameters and upload
// metadata reach the audit entry when body logging is on, along with the
// resolved route, and that image bytes are embedded only when input logging
// is enabled.
func TestImageEdits_AuditsRequestMetadata(t *testing.T) {
	tests := []struct {
		name            string
		logBodies       bool
		logImageInputs  bool
		logImageOutputs bool
	}{
		{name: "bodies off", logBodies: false},
		{name: "metadata only", logBodies: true},
		{name: "inputs only", logBodies: true, logImageInputs: true},
		{name: "outputs only", logBodies: true, logImageOutputs: true},
		{name: "all", logBodies: true, logImageInputs: true, logImageOutputs: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newImageEditMock()
			mock.resolved = &core.ModelSelector{Provider: "openai", Model: "gpt-image-1"}
			svc := &imageService{
				provider:        mock,
				logBodies:       tt.logBodies,
				logImageInputs:  tt.logImageInputs,
				logImageOutputs: tt.logImageOutputs,
			}
			c, rec := newImageEditRequest(t,
				[][2]string{{"model", "gpt-image-1"}, {"prompt", "add a hat"}, {"size", "1024x1024"}, {"provider", "openai"}},
				catPNG,
				editFormFile{field: "mask", filename: "mask.png", data: "mask-bytes"},
			)
			entry := &auditlog.LogEntry{}
			c.Set(string(auditlog.LogEntryKey), entry)

			if err := svc.CreateImageEdit(c); err != nil {
				t.Fatalf("CreateImageEdit returned error: %v", err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if entry.RequestedModel != "gpt-image-1" || entry.ResolvedModel != "openai/gpt-image-1" || entry.Provider != "mock" {
				t.Errorf("audit route = requested %q resolved %q provider %q", entry.RequestedModel, entry.ResolvedModel, entry.Provider)
			}
			if !tt.logBodies {
				if entry.Data != nil && (entry.Data.RequestBody != nil || entry.Data.ResponseBody != nil) {
					t.Fatalf("bodies captured although body logging is off: %+v", entry.Data)
				}
				return
			}

			reqBody, ok := entry.Data.RequestBody.(auditlog.ImageBodyLog)
			if !ok {
				t.Fatalf("request body = %T, want auditlog.ImageBodyLog", entry.Data.RequestBody)
			}
			if reqBody.Meta["prompt"] != "add a hat" || reqBody.Meta["size"] != "1024x1024" || reqBody.Meta["model"] != "gpt-image-1" {
				t.Errorf("audited request meta = %v", reqBody.Meta)
			}
			if _, present := reqBody.Meta["provider"]; present {
				t.Errorf("routing hint must not be audited: %v", reqBody.Meta)
			}
			if len(reqBody.Items) != 2 {
				t.Fatalf("audited uploads = %+v, want source and mask", reqBody.Items)
			}
			src, mask := reqBody.Items[0], reqBody.Items[1]
			if src.Role != "input" || src.Filename != "cat.png" || src.Bytes != len("cat-bytes") || mask.Role != "mask" || mask.Filename != "mask.png" {
				t.Errorf("audited uploads = %+v", reqBody.Items)
			}
			if src.Stored != tt.logImageInputs || mask.Stored != tt.logImageInputs {
				t.Errorf("upload bytes stored = %v/%v, want %v", src.Stored, mask.Stored, tt.logImageInputs)
			}

			respBody, ok := entry.Data.ResponseBody.(auditlog.ImageBodyLog)
			if !ok {
				t.Fatalf("response body = %T, want auditlog.ImageBodyLog", entry.Data.ResponseBody)
			}
			if len(respBody.Items) != 1 || respBody.Items[0].Role != "output" || respBody.Items[0].Stored != tt.logImageOutputs {
				t.Errorf("audited outputs = %+v, want one output stored=%v", respBody.Items, tt.logImageOutputs)
			}
			if usage, _ := respBody.Meta["usage"].(map[string]any); usage == nil || usage["total_tokens"] != 1050 {
				t.Errorf("audited response meta = %v, want usage envelope", respBody.Meta)
			}
		})
	}
}
