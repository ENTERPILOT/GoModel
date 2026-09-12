package openai

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedMultipart struct {
	values map[string][]string
	files  map[string][]capturedFile
}

type capturedFile struct {
	filename    string
	contentType string
	data        string
}

// recordedMultipart parses the multipart body of a recorded upstream request.
func recordedMultipart(t *testing.T, req providertest.Recorded) capturedMultipart {
	t.Helper()
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err, "upstream Content-Type")
	form, err := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"]).ReadForm(1 << 20)
	require.NoError(t, err, "upstream could not parse multipart body")
	t.Cleanup(func() { _ = form.RemoveAll() })

	got := capturedMultipart{values: form.Value, files: map[string][]capturedFile{}}
	for field, headers := range form.File {
		for _, h := range headers {
			f, err := h.Open()
			require.NoError(t, err)
			data, err := io.ReadAll(f)
			_ = f.Close()
			require.NoError(t, err)
			got.files[field] = append(got.files[field], capturedFile{
				filename: h.Filename, contentType: h.Header.Get("Content-Type"), data: string(data),
			})
		}
	}
	return got
}

func TestCreateImageEdit_ForwardsMultipartAndDecodesResponse(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(
		`{"created":1713833628,"data":[{"b64_json":"aGk="}],"usage":{"input_tokens":50,"output_tokens":1000,"total_tokens":1050,"input_tokens_details":{"text_tokens":10,"image_tokens":40}}}`))

	resp, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "add a hat",
		Images: []core.ImageFile{{Filename: "cat.png", ContentType: "image/png", Data: []byte("cat-bytes")}},
		Mask:   &core.ImageFile{Filename: "mask.png", ContentType: "image/png", Data: []byte("mask-bytes")},
		Fields: []core.FormField{{Name: "n", Value: "1"}, {Name: "size", Value: "1024x1024"}, {Name: "input_fidelity", Value: "high"}},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/images/edits", req.Path)
	got := recordedMultipart(t, req)
	for field, want := range map[string]string{"model": "gpt-image-1", "prompt": "add a hat", "n": "1", "size": "1024x1024", "input_fidelity": "high"} {
		assert.Equal(t, []string{want}, got.values[field], field)
	}
	assert.NotContains(t, got.values, "provider", "forwarded form carries provider hint")

	image := got.files["image"]
	require.Len(t, image, 1)
	assert.Equal(t, "cat.png", image[0].filename)
	assert.Equal(t, "image/png", image[0].contentType)
	assert.Equal(t, "cat-bytes", image[0].data)

	mask := got.files["mask"]
	require.Len(t, mask, 1)
	assert.Equal(t, "mask.png", mask[0].filename)
	assert.Equal(t, "mask-bytes", mask[0].data)
	assert.NotContains(t, got.files, "image[]", "single image must use the scalar field")

	assert.Equal(t, int64(1713833628), resp.Created)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aGk=", resp.Data[0].B64JSON)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 1050, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.InputTokensDetails)
	assert.Equal(t, 40, resp.Usage.InputTokensDetails.ImageTokens)
}

func TestCreateImageEdit_MultipleImagesUseArrayFieldAndDefaults(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(`{}`))

	resp, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "combine",
		Images: []core.ImageFile{{Data: []byte("one")}, {Filename: "two.jpg", ContentType: "image/jpeg", Data: []byte("two")}},
	})
	require.NoError(t, err)

	got := recordedMultipart(t, capture.Last(t))
	images := got.files["image[]"]
	require.Len(t, images, 2)
	assert.Equal(t, "image.png", images[0].filename, "filename should default")
	assert.Equal(t, "image/png", images[0].contentType, "content type should default")
	assert.Equal(t, "one", images[0].data)
	assert.Equal(t, "two.jpg", images[1].filename)
	assert.Equal(t, "image/jpeg", images[1].contentType)
	assert.Equal(t, "two", images[1].data)
	assert.NotContains(t, got.files, "mask")

	assert.NotZero(t, resp.Created)
	assert.NotNil(t, resp.Data)
}

func TestCreateImageEdit_RejectsInvalidRequests(t *testing.T) {
	provider, capture := newTestProvider(t, nil)

	tests := []struct {
		name    string
		req     *core.ImageEditRequest
		wantErr string
	}{
		{name: "nil", wantErr: "image edit request is required"},
		{name: "missing image", req: &core.ImageEditRequest{Model: "gpt-image-1", Prompt: "x"}, wantErr: "image is required"},
		{name: "stream", req: &core.ImageEditRequest{Model: "gpt-image-1", Prompt: "x", Images: []core.ImageFile{{Data: []byte("a")}}, Fields: []core.FormField{{Name: "stream", Value: "true"}}}, wantErr: "streaming image edits are not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateImageEdit(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
	assert.Zero(t, capture.Count(), "upstream must not be called for invalid requests")
}

func TestCreateImageEdit_PropagatesUpstreamError(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid image format","type":"invalid_request_error"}}`))
	})
	_, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model: "dall-e-2", Prompt: "x", Images: []core.ImageFile{{Data: []byte("a")}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid image format")
}

// TestCreateImageEdit_SanitizesPartMetadata ensures client-declared filenames
// and content types cannot smuggle CR/LF (header/part injection) or stray
// quotes into the upstream multipart headers.
func TestCreateImageEdit_SanitizesPartMetadata(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(`{}`))

	_, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "x",
		Images: []core.ImageFile{{
			Filename:    "evil\r\nContent-Type: text/html\r\n\r\n.png\"",
			ContentType: "image/png\r\nX-Smuggled: 1",
			Data:        []byte("bytes"),
		}},
	})
	require.NoError(t, err)

	image := recordedMultipart(t, capture.Last(t)).files["image"]
	require.Len(t, image, 1)
	assert.False(t, strings.ContainsAny(image[0].filename, "\r\n"), "CR/LF reached the upstream headers: %+v", image[0])
	assert.False(t, strings.ContainsAny(image[0].contentType, "\r\n"), "CR/LF reached the upstream headers: %+v", image[0])

	if image[0].contentType != "image/pngX-Smuggled: 1" && image[0].contentType != "image/png" {
		// The exact folded remainder is unimportant; the header must stay one line.
		t.Logf("content type folded to %q", image[0].contentType)
	}
	assert.Equal(t, "bytes", image[0].data)
}
