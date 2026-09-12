package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
		err := w.WriteField(kv[0], kv[1])
		require.NoError(t, err)
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="`+f.field+`"; filename="`+f.filename+`"`)
		if f.contentType != "" {
			h.Set("Content-Type", f.contentType)
		}
		part, err := w.CreatePart(h)
		require.NoError(t, err)

		_, _ = part.Write([]byte(f.data))
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func newImageEditRequest(t *testing.T, values [][2]string, files ...editFormFile) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body, contentType := editForm(t, values, files...)
	return echotest.Post(t, "/v1/images/edits", body, echotest.WithContentType(contentType))
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
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := echotest.Decode[map[string]any](t, rec)

	data, _ := got["data"].([]any)
	require.Len(t, data, 1)
	image, _ := data[0].(map[string]any)
	assert.Equal(t, "aGk=", image["b64_json"], "data[0] = %v, want b64_json aGk=", data[0])

	req := mock.capturedEdit
	require.NotNil(t, req)
	assert.Equal(t, "gpt-image-1", req.Model)
	assert.Empty(t, req.Provider)
	assert.Equal(t, "add a hat", req.Prompt, "provider saw %+v", req)
	assert.Len(t, req.Images, 1)
	assert.Equal(t, "cat.png", req.Images[0].Filename)
	assert.Equal(t, "image/png", req.Images[0].ContentType)
	assert.Equal(t, "cat-bytes", string(req.Images[0].Data))
	assert.NotNil(t, req.Mask)
	assert.Equal(t, "mask-bytes", string(req.Mask.Data))

	want := []core.FormField{{Name: "input_fidelity", Value: "high"}, {Name: "n", Value: "1"}, {Name: "size", Value: "1024x1024"}}
	require.Len(t, req.Fields, len(want))

	for i := range want {
		assert.Equal(t, want[i], req.Fields[i], "fields[%d] = %+v, want %+v", i, req.Fields[i], want[i])
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
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	req := mock.capturedEdit
	assert.NotNil(t, req)
	assert.Len(t, req.Images, 2)
	assert.Equal(t, "one", string(req.Images[0].Data))
	assert.Equal(t, "two", string(req.Images[1].Data), "images = %+v", mock.capturedEdit)
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
			err := svc.CreateImageEdit(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantMsg)
			assert.Nil(t, mock.capturedEdit)
		})
	}
}

func TestImageEdits_RejectsNonMultipartBody(t *testing.T) {
	svc := &imageService{provider: newImageEditMock()}
	c, rec := echotest.Post(t, "/v1/images/edits", `{"model":"gpt-image-1","prompt":"x"}`)
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid multipart form")
}

func TestImageEdits_RouterWithoutEditSupport(t *testing.T) {
	// A router that generates images but cannot edit them.
	svc := &imageService{provider: newImageMock()}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "dall-e-3"}, {"prompt", "x"}}, catPNG)
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "image edits are not supported")
}

func TestImageEdits_AuthorizesResolvedSelector(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		mock := newImageEditMock()
		mock.resolved = &core.ModelSelector{Provider: "openai", Model: "gpt-image-1"}
		authorizer := &recordingModelAuthorizer{}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)
		err := svc.CreateImageEdit(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "openai", authorizer.lastSelector.Provider)
		assert.Equal(t, "gpt-image-1", authorizer.lastSelector.Model, "authorizer saw %+v, want resolved openai/gpt-image-1", authorizer.lastSelector)
	})

	t.Run("denied", func(t *testing.T) {
		mock := newImageEditMock()
		authorizer := &recordingModelAuthorizer{err: core.NewInvalidRequestError("denied", nil)}
		svc := &imageService{provider: mock, modelAuthorizer: authorizer}
		c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)
		err := svc.CreateImageEdit(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Nil(t, mock.capturedEdit)
	})
}

func TestImageEdits_ProviderErrorIsSurfaced(t *testing.T) {
	mock := newImageEditMock()
	mock.imageErr = core.NewProviderError("openai", http.StatusBadRequest, "Invalid image format", nil)
	svc := &imageService{provider: mock}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid image format")
}

func TestImageEdits_NilProviderResponseIs502(t *testing.T) {
	mock := newImageEditMock()
	mock.imageResp = nil
	mock.providerNames = map[string]string{"gpt-image-1": "image-primary"}
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	svc := &imageService{provider: mock, usageLogger: logger}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"provider":"image-primary"`)
	assert.Contains(t, rec.Body.String(), "provider image-primary returned empty image response")
	assert.Nil(t, captured)
}

func TestImageEdits_LogsUsage(t *testing.T) {
	var captured *usage.UsageEntry
	logger := &capturingUsageLogger{config: usage.Config{Enabled: true}, captured: &captured}
	mock := newImageEditMock()
	// gpt-image-1 reports image output tokens, priced at $40/Mtok: the mock's
	// 1000 output tokens cost $0.04.
	pricing := &core.ModelPricing{OutputImagePerMtok: new(40.0)}
	svc := &imageService{
		provider:        mock,
		usageLogger:     logger,
		pricingResolver: &mockPricingResolver{pricing: pricing}}
	c, rec := newImageEditRequest(t, [][2]string{{"model", "gpt-image-1"}, {"prompt", "x"}}, catPNG)
	err := svc.CreateImageEdit(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, captured)
	assert.Equal(t, "/v1/images/edits", captured.Endpoint)
	assert.Equal(t, "gpt-image-1", captured.Model)
	assert.Equal(t, 1050, captured.TotalTokens)
	got := captured.RawData["images"]
	assert.Equal(t, 1, got)
	assert.NotNil(t, captured.TotalCost)
	assert.GreaterOrEqual(t, *captured.TotalCost, 0.0399)
	assert.LessOrEqual(t, *captured.TotalCost, 0.0401)
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

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := echotest.Decode[map[string]any](t, rec)
	data, _ := got["data"].([]any)
	require.Len(t, data, 1)
	assert.NotNil(t, mock.capturedEdit)
	assert.Equal(t, "add a hat", mock.capturedEdit.Prompt)
	assert.Len(t, mock.capturedEdit.Images, 1)
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
			err := svc.CreateImageEdit(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "gpt-image-1", entry.RequestedModel)
			assert.Equal(t, "openai/gpt-image-1", entry.ResolvedModel)
			assert.Equal(t, "mock", entry.Provider)

			if !tt.logBodies {
				if entry.Data != nil {
					require.Nil(t, entry.Data.RequestBody, "body logging is off")
					require.Nil(t, entry.Data.ResponseBody, "body logging is off")
				}
				return
			}

			reqBody, ok := entry.Data.RequestBody.(auditlog.ImageBodyLog)
			require.True(t, ok, "request body = %T, want auditlog.ImageBodyLog", entry.Data.RequestBody)
			assert.Equal(t, "add a hat", reqBody.Meta["prompt"])
			assert.Equal(t, "1024x1024", reqBody.Meta["size"])
			assert.Equal(t, "gpt-image-1", reqBody.Meta["model"], "audited request meta = %v", reqBody.Meta)
			_, present := reqBody.Meta["provider"]
			assert.False(t, present, "routing hint must not be audited: %v", reqBody.Meta)
			require.Len(t, reqBody.Items, 2)

			src, mask := reqBody.Items[0], reqBody.Items[1]
			assert.Equal(t, "input", src.Role)
			assert.Equal(t, "cat.png", src.Filename)
			assert.Equal(t, len("cat-bytes"), src.Bytes)
			assert.Equal(t, "mask", mask.Role)
			assert.Equal(t, "mask.png", mask.Filename, "audited uploads = %+v", reqBody.Items)
			assert.Equal(t, tt.logImageInputs, src.Stored)
			assert.Equal(t, tt.logImageInputs, mask.Stored)

			respBody, ok := entry.Data.ResponseBody.(auditlog.ImageBodyLog)
			require.True(t, ok, "response body = %T, want auditlog.ImageBodyLog", entry.Data.ResponseBody)
			assert.Len(t, respBody.Items, 1)
			assert.Equal(t, "output", respBody.Items[0].Role)
			assert.Equal(t, tt.logImageOutputs, respBody.Items[0].Stored)
			usage, _ := respBody.Meta["usage"].(map[string]any)
			assert.NotNil(t, usage)
			assert.Equal(t, 1050, usage["total_tokens"])
		})
	}
}
