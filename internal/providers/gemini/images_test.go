package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func decodeImageRequest(t *testing.T, body string) *core.ImageGenerationRequest {
	t.Helper()
	req, err := core.DecodeImageGenerationRequest([]byte(body), nil)
	if err != nil {
		t.Fatalf("decode image request: %v", err)
	}
	return req
}

func TestCreateImage_ImagenPredict(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/imagen-4.0-generate-001:predict" {
			t.Errorf("path = %q, want /v1beta/models/imagen-4.0-generate-001:predict", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"predictions": [
			{"bytesBase64Encoded": "aW1nMQ==", "mimeType": "image/png", "prompt": "an enhanced prompt"},
			{"bytesBase64Encoded": "aW1nMg==", "mimeType": "image/png"}
		]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	req := decodeImageRequest(t, `{
		"model": "imagen-4.0-generate-001",
		"prompt": "a lighthouse at dawn",
		"n": 2,
		"size": "1024x1024",
		"personGeneration": "allow_adult"
	}`)
	resp, err := p.CreateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	instances, _ := gotBody["instances"].([]any)
	if len(instances) != 1 || instances[0].(map[string]any)["prompt"] != "a lighthouse at dawn" {
		t.Errorf("instances = %+v, want single prompt instance", gotBody["instances"])
	}
	parameters, _ := gotBody["parameters"].(map[string]any)
	if parameters["sampleCount"] != float64(2) {
		t.Errorf("sampleCount = %v, want 2", parameters["sampleCount"])
	}
	if parameters["aspectRatio"] != "1:1" {
		t.Errorf("aspectRatio = %v, want 1:1", parameters["aspectRatio"])
	}
	if parameters["personGeneration"] != "allow_adult" {
		t.Errorf("personGeneration = %v, want extra field forwarded verbatim", parameters["personGeneration"])
	}

	if len(resp.Data) != 2 {
		t.Fatalf("data = %+v, want 2 images", resp.Data)
	}
	if resp.Data[0].B64JSON != "aW1nMQ==" || resp.Data[0].RevisedPrompt != "an enhanced prompt" {
		t.Errorf("first image = %+v, want b64 bytes and revised prompt", resp.Data[0])
	}
	if resp.Provider != "gemini" || resp.Created == 0 {
		t.Errorf("provider/created = %q/%d, want gemini and non-zero created", resp.Provider, resp.Created)
	}
}

func TestCreateImage_ImagenAllFilteredIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"predictions": [{"raiFilteredReason": "blocked by safety filters"}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x"}`))
	if err == nil || !strings.Contains(err.Error(), "blocked by safety filters") {
		t.Fatalf("error = %v, want filtered reason surfaced", err)
	}
}

func TestCreateImage_GeminiImageModelGenerateContent(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash-image:generateContent" {
			t.Errorf("path = %q, want /v1beta/models/gemini-2.5-flash-image:generateContent", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [
					{"text": "Here is your image."},
					{"inlineData": {"mimeType": "image/png", "data": "aW1n"}}
				]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 1290, "totalTokenCount": 1300}
		}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	req := decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "a lighthouse", "size": "1536x1024"}`)
	resp, err := p.CreateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contents, _ := gotBody["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %+v, want one user turn", gotBody["contents"])
	}
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "a lighthouse" {
		t.Errorf("parts = %+v, want single prompt text part", parts)
	}
	config, _ := gotBody["generationConfig"].(map[string]any)
	modalities, _ := config["responseModalities"].([]any)
	if len(modalities) != 2 || modalities[0] != "TEXT" || modalities[1] != "IMAGE" {
		t.Errorf("responseModalities = %+v, want [TEXT IMAGE]", config["responseModalities"])
	}
	imageConfig, _ := config["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "3:2" {
		t.Errorf("imageConfig = %+v, want aspectRatio 3:2 for 1536x1024", config["imageConfig"])
	}
	if _, ok := config["candidateCount"]; ok {
		t.Errorf("candidateCount present for n=1: %+v", config)
	}

	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aW1n" {
		t.Fatalf("data = %+v, want one inline image", resp.Data)
	}
	if resp.Data[0].RevisedPrompt != "Here is your image." {
		t.Errorf("revised_prompt = %q, want model text surfaced", resp.Data[0].RevisedPrompt)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 1290 || resp.Usage.TotalTokens != 1300 {
		t.Errorf("usage = %+v, want mapped token counts", resp.Usage)
	}
}

func TestCreateImage_GeminiImageModelNoImageIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"role": "model", "parts": [{"text": "I cannot draw that."}]}, "finishReason": "STOP"}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x"}`))
	if err == nil || !strings.Contains(err.Error(), "I cannot draw that.") {
		t.Fatalf("error = %v, want model text surfaced", err)
	}
}

func TestCreateImage_CompatModeUsesOpenAIEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/images/generations" {
			t.Errorf("path = %q, want /v1beta/openai/images/generations", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want Bearer key on compat surface", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created": 1700000000, "data": [{"b64_json": "aW1n"}]}`))
	}))
	defer server.Close()

	p := newCompatTestProvider(t, server)
	resp, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aW1n" || resp.Provider != "gemini" {
		t.Fatalf("response = %+v, want compat passthrough with provider stamp", resp)
	}
}

func TestCreateImageEdit_GenerateContent(t *testing.T) {
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash-image:generateContent" {
			t.Errorf("path = %q, want generateContent on the edit model", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"role": "model", "parts": [{"inline_data": {"mime_type": "image/png", "data": "ZWRpdGVk"}}]}, "finishReason": "STOP"}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	resp, err := p.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gemini-2.5-flash-image",
		Prompt: "add a sailboat",
		Images: []core.ImageFile{
			{Filename: "a.png", ContentType: "image/png", Data: imageBytes},
			{Filename: "b.jpg", ContentType: "image/jpeg; charset=binary", Data: []byte{0xff}},
		},
		Fields: []core.FormField{{Name: "size", Value: "1024x1536"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	contents, _ := gotBody["contents"].([]any)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want two images then the prompt", parts)
	}
	first, _ := parts[0].(map[string]any)["inline_data"].(map[string]any)
	if first["mime_type"] != "image/png" || first["data"] != base64.StdEncoding.EncodeToString(imageBytes) {
		t.Errorf("first inline part = %+v, want base64 png upload", first)
	}
	second, _ := parts[1].(map[string]any)["inline_data"].(map[string]any)
	if second["mime_type"] != "image/jpeg" {
		t.Errorf("second inline mime = %v, want media type without parameters", second["mime_type"])
	}
	if parts[2].(map[string]any)["text"] != "add a sailboat" {
		t.Errorf("last part = %+v, want the prompt text", parts[2])
	}
	config, _ := gotBody["generationConfig"].(map[string]any)
	imageConfig, _ := config["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "2:3" {
		t.Errorf("imageConfig = %+v, want aspectRatio 2:3 for 1024x1536", config["imageConfig"])
	}

	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "ZWRpdGVk" {
		t.Fatalf("data = %+v, want the edited image", resp.Data)
	}
}

func TestCreateImageEdit_Rejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no upstream call expected")
	}))
	defer server.Close()

	baseRequest := func() *core.ImageEditRequest {
		return &core.ImageEditRequest{
			Model:  "gemini-2.5-flash-image",
			Prompt: "edit",
			Images: []core.ImageFile{{Filename: "a.png", Data: []byte{1}}},
		}
	}

	t.Run("mask unsupported", func(t *testing.T) {
		p := newNativeTestProvider(t, server)
		req := baseRequest()
		req.Mask = &core.ImageFile{Filename: "mask.png", Data: []byte{1}}
		_, err := p.CreateImageEdit(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "mask") {
			t.Fatalf("error = %v, want mask rejection", err)
		}
	})

	t.Run("imagen models cannot edit", func(t *testing.T) {
		p := newNativeTestProvider(t, server)
		req := baseRequest()
		req.Model = "imagen-4.0-generate-001"
		_, err := p.CreateImageEdit(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "does not support image edits") {
			t.Fatalf("error = %v, want imagen edit rejection", err)
		}
	})

	t.Run("compat mode requires native API", func(t *testing.T) {
		p := newCompatTestProvider(t, server)
		_, err := p.CreateImageEdit(context.Background(), baseRequest())
		if err == nil || !strings.Contains(err.Error(), "native API mode") {
			t.Fatalf("error = %v, want native-mode requirement", err)
		}
	})
}

func TestImageAspectRatio(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"", ""},
		{"auto", ""},
		{"1024x1024", "1:1"},
		{"1536x1024", "3:2"},
		{"1024x1536", "2:3"},
		{"1792x1024", "16:9"},
		{"1024x1792", "9:16"},
		{"16:9", "16:9"},
		{"512X512", "1:1"},
		{"bogus", ""},
		{"0x100", ""},
	}
	for _, tt := range tests {
		if got := imageAspectRatio(tt.size); got != tt.want {
			t.Errorf("imageAspectRatio(%q) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestCreateImage_GeminiImageModelMultiImage(t *testing.T) {
	// Gemini image models reject candidateCount > 1, so n is served as n
	// parallel single-candidate calls whose results merge.
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		config, _ := got["generationConfig"].(map[string]any)
		if _, ok := config["candidateCount"]; ok {
			t.Errorf("candidateCount sent upstream: %+v", config)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{
			"candidates": [{"content": {"role": "model", "parts": [{"inlineData": {"mimeType": "image/png", "data": "aW1nJTAxZA=="}}]}, "finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 1290, "totalTokenCount": 1298}
		}`)
		_ = call
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	resp, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "two variants", "n": 2}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want one per requested image", got)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data = %+v, want both fan-out results merged", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 16 || resp.Usage.OutputTokens != 2580 || resp.Usage.TotalTokens != 2596 {
		t.Errorf("usage = %+v, want summed token counts", resp.Usage)
	}
}

func TestCreateImage_GeminiImageModelFanOutCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no upstream call expected above the fan-out cap")
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x", "n": 11}`))
	if err == nil || !strings.Contains(err.Error(), "at most 10") {
		t.Fatalf("error = %v, want fan-out cap rejection", err)
	}
}

func TestCreateImage_GeminiImageModelFanOutFailureFails(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates": [{"content": {"role": "model", "parts": [{"inlineData": {"mimeType": "image/png", "data": "aW1n"}}]}, "finishReason": "STOP"}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "boom"}}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x", "n": 2}`))
	if err == nil {
		t.Fatal("expected a failed fan-out call to fail the request")
	}
}

func TestCreateImage_ImagenNativeSampleCountWins(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"predictions": [{"bytesBase64Encoded": "aW1n"}]}`))
	}))
	defer server.Close()

	p := newNativeTestProvider(t, server)
	// A client speaking native Imagen sends sampleCount directly; the OpenAI
	// n default must not overwrite it (same precedence rule as aspectRatio).
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x", "sampleCount": 3}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parameters, _ := gotBody["parameters"].(map[string]any)
	if parameters["sampleCount"] != float64(3) {
		t.Errorf("sampleCount = %v, want client-sent 3 preserved", parameters["sampleCount"])
	}
}
