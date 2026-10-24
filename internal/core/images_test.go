package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeImageGenerationRequest_PreservesExtraFields(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"a cat","n":2,"size":"1024x1024","quality":"high","provider":"openai","background":"transparent","output_format":"webp","moderation":"low"}`)

	req, err := DecodeImageGenerationRequest(body, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-1", req.Model)
	require.Equal(t, "a cat", req.Prompt)
	require.Equal(t, "1024x1024", req.Size)
	require.Equal(t, "high", req.Quality)
	require.Equal(t, "openai", req.Provider, "typed fields = %+v", req)
	require.Equal(t, 2, req.ImageCount())

	// Marshal round-trip: unknown fields are forwarded upstream verbatim and
	// the gateway-only provider hint is dropped once cleared.
	req.Provider = ""
	out, err := json.Marshal(req)
	require.NoError(t, err)

	var forwarded map[string]any
	err = json.Unmarshal(out, &forwarded)
	require.NoError(t, err)

	for key, want := range map[string]any{
		"model": "gpt-image-1", "prompt": "a cat", "n": float64(2), "size": "1024x1024",
		"quality": "high", "background": "transparent", "output_format": "webp", "moderation": "low",
	} {
		got := forwarded[key]
		assert.Equal(t, want, got)
	}
	_, present := forwarded["provider"]
	assert.False(t, present, "forwarded body still carries provider: %s", out)
}

func TestDecodeImageGenerationRequest_RejectsMalformedJSON(t *testing.T) {
	_, err := DecodeImageGenerationRequest([]byte(`{"model":`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid image generation request")
}

func TestImageGenerationRequest_ImageCountDefaultsToOne(t *testing.T) {
	var nilReq *ImageGenerationRequest
	got := nilReq.ImageCount()
	assert.Equal(t, 1, got)
	got = (&ImageGenerationRequest{}).ImageCount()
	assert.Equal(t, 1, got)
}

func TestValidateImageGenerationRequest(t *testing.T) {
	zero := 0
	tests := []struct {
		name    string
		req     *ImageGenerationRequest
		wantErr string
	}{
		{"nil request", nil, "image generation request is required"},
		{"missing model", &ImageGenerationRequest{Model: " ", Prompt: "a cat"}, "model is required"},
		{"missing prompt", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "  "}, "prompt is required"},
		{"zero n", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat", N: &zero}, "n must be at least 1"},
		{"streaming", &ImageGenerationRequest{Model: "gpt-image-1", Prompt: "a cat", Stream: true}, "streaming image generation is not supported"},
		{"valid", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImageGenerationRequest(tt.req)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestImageGenerationResponse_OmitsEmptyOptionalFields(t *testing.T) {
	out, err := json.Marshal(&ImageGenerationResponse{Created: 1, Data: []ImageData{{URL: "https://img"}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"created":1,"data":[{"url":"https://img"}]}`, string(out))
}
