package gemini

import (
	"context"
	"encoding/base64"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeImageRequest(t *testing.T, body string) *core.ImageGenerationRequest {
	t.Helper()
	req, err := core.DecodeImageGenerationRequest([]byte(body), nil)
	require.NoError(t, err)

	return req
}

func TestCreateImage_ImagenPredict(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"predictions": [
		{"bytesBase64Encoded": "aW1nMQ==", "mimeType": "image/png", "prompt": "an enhanced prompt"},
		{"bytesBase64Encoded": "aW1nMg==", "mimeType": "image/png"}
	]}`)

	p := newNativeTestProvider(t, server)
	req := decodeImageRequest(t, `{
		"model": "imagen-4.0-generate-001",
		"prompt": "a lighthouse at dawn",
		"n": 2,
		"size": "1024x1024",
		"personGeneration": "allow_adult"
	}`)
	resp, err := p.CreateImage(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models/imagen-4.0-generate-001:predict", sent.Path)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-goog-api-key"))

	body := sent.JSON(t)
	instances, _ := body["instances"].([]any)
	require.Len(t, instances, 1)
	assert.Equal(t, "a lighthouse at dawn", instances[0].(map[string]any)["prompt"])

	parameters, _ := body["parameters"].(map[string]any)
	assert.Equal(t, float64(2), parameters["sampleCount"])
	assert.Equal(t, "1:1", parameters["aspectRatio"])
	assert.Equal(t, "allow_adult", parameters["personGeneration"])

	require.Len(t, resp.Data, 2)
	assert.Equal(t, "aW1nMQ==", resp.Data[0].B64JSON)
	assert.Equal(t, "an enhanced prompt", resp.Data[0].RevisedPrompt)
	assert.Equal(t, "gemini", resp.Provider)
	assert.NotZero(t, resp.Created)
}

func TestCreateImage_ImagenAllFilteredIsError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"predictions": [{"raiFilteredReason": "blocked by safety filters"}]}`)

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked by safety filters")
}

func TestCreateImage_GeminiImageModelGenerateContent(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "Here is your image."},
				{"inlineData": {"mimeType": "image/png", "data": "aW1n"}}
			]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 1290, "totalTokenCount": 1300}
	}`)

	p := newNativeTestProvider(t, server)
	req := decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "a lighthouse", "size": "1536x1024"}`)
	resp, err := p.CreateImage(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash-image:generateContent", sent.Path)

	body := sent.JSON(t)
	contents, _ := body["contents"].([]any)
	require.Len(t, contents, 1)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	require.Len(t, parts, 1)
	assert.Equal(t, "a lighthouse", parts[0].(map[string]any)["text"])

	config, _ := body["generationConfig"].(map[string]any)
	assert.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
	imageConfig, _ := config["imageConfig"].(map[string]any)
	assert.Equal(t, "3:2", imageConfig["aspectRatio"], "1536x1024 maps to 3:2")
	assert.NotContains(t, config, "candidateCount", "candidateCount must be absent for n=1")

	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aW1n", resp.Data[0].B64JSON)
	assert.Equal(t, "Here is your image.", resp.Data[0].RevisedPrompt)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 1290, resp.Usage.OutputTokens)
	assert.Equal(t, 1300, resp.Usage.TotalTokens)
}

func TestCreateImage_GeminiImageModelNoImageIsError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"candidates": [{"content": {"role": "model", "parts": [{"text": "I cannot draw that."}]}, "finishReason": "STOP"}]}`)

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "I cannot draw that.")
}

func TestCreateImage_CompatModeUsesOpenAIEndpoint(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"created": 1700000000, "data": [{"b64_json": "aW1n"}]}`)

	p := newCompatTestProvider(t, server)
	resp, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x"}`))
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/openai/images/generations", sent.Path)
	assert.Equal(t, "Bearer test-api-key", sent.Header.Get("Authorization"))

	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aW1n", resp.Data[0].B64JSON)
	assert.Equal(t, "gemini", resp.Provider)
}

func TestCreateImageEdit_GenerateContent(t *testing.T) {
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"candidates": [{"content": {"role": "model", "parts": [{"inline_data": {"mime_type": "image/png", "data": "ZWRpdGVk"}}]}, "finishReason": "STOP"}]}`)

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
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash-image:generateContent", sent.Path)

	body := sent.JSON(t)
	contents, _ := body["contents"].([]any)
	require.Len(t, contents, 1)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	require.Len(t, parts, 3)

	first, _ := parts[0].(map[string]any)["inline_data"].(map[string]any)
	assert.Equal(t, "image/png", first["mime_type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(imageBytes), first["data"])

	second, _ := parts[1].(map[string]any)["inline_data"].(map[string]any)
	assert.Equal(t, "image/jpeg", second["mime_type"], "content type parameters are stripped")
	assert.Equal(t, "add a sailboat", parts[2].(map[string]any)["text"], "prompt is the last part")

	config, _ := body["generationConfig"].(map[string]any)
	imageConfig, _ := config["imageConfig"].(map[string]any)
	assert.Equal(t, "2:3", imageConfig["aspectRatio"], "1024x1536 maps to 2:3")

	require.Len(t, resp.Data, 1)
	assert.Equal(t, "ZWRpdGVk", resp.Data[0].B64JSON)
}

func TestCreateImageEdit_Rejections(t *testing.T) {
	server, capture := providertest.Server(t, nil)

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
		require.Error(t, err)
		require.Contains(t, err.Error(), "mask")
	})

	t.Run("imagen models cannot edit", func(t *testing.T) {
		p := newNativeTestProvider(t, server)
		req := baseRequest()
		req.Model = "imagen-4.0-generate-001"
		_, err := p.CreateImageEdit(context.Background(), req)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not support image edits")
	})

	t.Run("compat mode requires native API", func(t *testing.T) {
		p := newCompatTestProvider(t, server)
		_, err := p.CreateImageEdit(context.Background(), baseRequest())
		require.Error(t, err)
		require.Contains(t, err.Error(), "native API mode")
	})

	assert.Zero(t, capture.Count(), "no upstream call expected")
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
		assert.Equal(t, tt.want, imageAspectRatio(tt.size), "size %q", tt.size)
	}
}

func TestCreateImage_GeminiImageModelMultiImage(t *testing.T) {
	// Gemini image models reject candidateCount > 1, so n is served as n
	// parallel single-candidate calls whose results merge.
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"candidates": [{"content": {"role": "model", "parts": [{"inlineData": {"mimeType": "image/png", "data": "aW1nJTAxZA=="}}]}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 1290, "totalTokenCount": 1298}
	}`)

	p := newNativeTestProvider(t, server)
	resp, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "two variants", "n": 2}`))
	require.NoError(t, err)

	require.Equal(t, 2, capture.Count())
	for _, sent := range capture.All() {
		config, _ := sent.JSON(t)["generationConfig"].(map[string]any)
		assert.NotContains(t, config, "candidateCount", "candidateCount must not be sent upstream")
	}

	require.Len(t, resp.Data, 2)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 16, resp.Usage.InputTokens)
	assert.Equal(t, 2580, resp.Usage.OutputTokens)
	assert.Equal(t, 2596, resp.Usage.TotalTokens)
}

func TestCreateImage_GeminiImageModelFanOutCap(t *testing.T) {
	server, capture := providertest.Server(t, nil)

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x", "n": 11}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most 10")
	assert.Zero(t, capture.Count(), "no upstream call expected above the fan-out cap")
}

func TestCreateImage_GeminiImageModelFanOutFailureFails(t *testing.T) {
	var calls atomic.Int32
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates": [{"content": {"role": "model", "parts": [{"inlineData": {"mimeType": "image/png", "data": "aW1n"}}]}, "finishReason": "STOP"}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "boom"}}`))
	})

	p := newNativeTestProvider(t, server)
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "gemini-2.5-flash-image", "prompt": "x", "n": 2}`))
	require.Error(t, err)
}

func TestCreateImage_ImagenNativeSampleCountWins(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"predictions": [{"bytesBase64Encoded": "aW1n"}]}`)

	p := newNativeTestProvider(t, server)
	// A client speaking native Imagen sends sampleCount directly; the OpenAI
	// n default must not overwrite it (same precedence rule as aspectRatio).
	_, err := p.CreateImage(context.Background(), decodeImageRequest(t, `{"model": "imagen-4.0-generate-001", "prompt": "x", "sampleCount": 3}`))
	require.NoError(t, err)

	parameters, _ := capture.Last(t).JSON(t)["parameters"].(map[string]any)
	assert.Equal(t, float64(3), parameters["sampleCount"])
}
