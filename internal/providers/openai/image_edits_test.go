package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type capturedMultipart struct {
	path   string
	values map[string][]string
	files  map[string][]capturedFile
}

type capturedFile struct {
	filename    string
	contentType string
	data        string
}

// captureMultipartHandler records the multipart request the adapter sends and
// answers with body.
func captureMultipartHandler(t *testing.T, got *capturedMultipart, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("upstream could not parse multipart body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got.values = r.MultipartForm.Value
		got.files = map[string][]capturedFile{}
		for field, headers := range r.MultipartForm.File {
			for _, h := range headers {
				f, _ := h.Open()
				data, _ := io.ReadAll(f)
				_ = f.Close()
				got.files[field] = append(got.files[field], capturedFile{
					filename: h.Filename, contentType: h.Header.Get("Content-Type"), data: string(data),
				})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestCreateImageEdit_ForwardsMultipartAndDecodesResponse(t *testing.T) {
	var got capturedMultipart
	provider := newSpeechTestProvider(t, captureMultipartHandler(t, &got,
		`{"created":1713833628,"data":[{"b64_json":"aGk="}],"usage":{"input_tokens":50,"output_tokens":1000,"total_tokens":1050,"input_tokens_details":{"text_tokens":10,"image_tokens":40}}}`))

	resp, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "add a hat",
		Images: []core.ImageFile{{Filename: "cat.png", ContentType: "image/png", Data: []byte("cat-bytes")}},
		Mask:   &core.ImageFile{Filename: "mask.png", ContentType: "image/png", Data: []byte("mask-bytes")},
		Fields: []core.FormField{{Name: "n", Value: "1"}, {Name: "size", Value: "1024x1024"}, {Name: "input_fidelity", Value: "high"}},
	})
	if err != nil {
		t.Fatalf("CreateImageEdit() error = %v", err)
	}

	if got.path != "/images/edits" {
		t.Errorf("path = %q, want /images/edits", got.path)
	}
	for field, want := range map[string]string{"model": "gpt-image-1", "prompt": "add a hat", "n": "1", "size": "1024x1024", "input_fidelity": "high"} {
		if v := got.values[field]; len(v) != 1 || v[0] != want {
			t.Errorf("form %s = %v, want %q", field, v, want)
		}
	}
	if _, present := got.values["provider"]; present {
		t.Errorf("forwarded form carries provider hint: %v", got.values)
	}
	image := got.files["image"]
	if len(image) != 1 || image[0].filename != "cat.png" || image[0].contentType != "image/png" || image[0].data != "cat-bytes" {
		t.Errorf("image part = %+v", image)
	}
	mask := got.files["mask"]
	if len(mask) != 1 || mask[0].filename != "mask.png" || mask[0].data != "mask-bytes" {
		t.Errorf("mask part = %+v", mask)
	}
	if _, present := got.files["image[]"]; present {
		t.Error("single image must be sent as image, not image[]")
	}

	if resp.Created != 1713833628 || len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 1050 || resp.Usage.InputTokensDetails == nil || resp.Usage.InputTokensDetails.ImageTokens != 40 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestCreateImageEdit_MultipleImagesUseArrayFieldAndDefaults(t *testing.T) {
	var got capturedMultipart
	provider := newSpeechTestProvider(t, captureMultipartHandler(t, &got, `{}`))

	resp, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "combine",
		Images: []core.ImageFile{{Data: []byte("one")}, {Filename: "two.jpg", ContentType: "image/jpeg", Data: []byte("two")}},
	})
	if err != nil {
		t.Fatalf("CreateImageEdit() error = %v", err)
	}
	images := got.files["image[]"]
	if len(images) != 2 {
		t.Fatalf("image[] parts = %+v, want 2", images)
	}
	if images[0].filename != "image.png" || images[0].contentType != "image/png" || images[0].data != "one" {
		t.Errorf("defaulted part = %+v", images[0])
	}
	if images[1].filename != "two.jpg" || images[1].contentType != "image/jpeg" || images[1].data != "two" {
		t.Errorf("second part = %+v", images[1])
	}
	if _, present := got.files["mask"]; present {
		t.Error("mask part must be omitted when not supplied")
	}
	if resp.Created == 0 {
		t.Error("Created should default to now when upstream omits it")
	}
	if resp.Data == nil {
		t.Error("Data should be an empty array, not null")
	}
}

func TestCreateImageEdit_RejectsInvalidRequests(t *testing.T) {
	provider := newSpeechTestProvider(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not be called for invalid requests")
	})

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
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCreateImageEdit_PropagatesUpstreamError(t *testing.T) {
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid image format","type":"invalid_request_error"}}`))
	})
	_, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model: "dall-e-2", Prompt: "x", Images: []core.ImageFile{{Data: []byte("a")}},
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid image format") {
		t.Fatalf("error = %v, want upstream message", err)
	}
}
