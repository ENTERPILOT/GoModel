package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateImage_ForwardsRequestAndDecodesResponse(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(
		`{"created":1713833628,"data":[{"b64_json":"aGk=","revised_prompt":"a fluffy cat"}],"output_format":"png","quality":"high","size":"1024x1024","usage":{"input_tokens":10,"output_tokens":1000,"total_tokens":1010,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`))

	req, err := core.DecodeImageGenerationRequest([]byte(`{"model":"gpt-image-1","prompt":"a cat","n":1,"quality":"high","background":"opaque"}`), nil)
	require.NoError(t, err)

	resp, err := provider.CreateImage(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/images/generations", sent.Path)
	body := sent.JSON(t)
	assert.Equal(t, "gpt-image-1", body["model"])
	assert.Equal(t, "a cat", body["prompt"])
	assert.Equal(t, "opaque", body["background"], "unknown fields must be forwarded")
	assert.NotContains(t, body, "provider", "forwarded body carries provider hint")

	assert.Equal(t, int64(1713833628), resp.Created)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aGk=", resp.Data[0].B64JSON)
	assert.Equal(t, "a fluffy cat", resp.Data[0].RevisedPrompt)
	assert.Equal(t, "high", resp.Quality)
	assert.Equal(t, "1024x1024", resp.Size)
	assert.Equal(t, "png", resp.OutputFormat)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 1010, resp.Usage.TotalTokens)
	require.NotNil(t, resp.Usage.InputTokensDetails)
	assert.Equal(t, 10, resp.Usage.InputTokensDetails.TextTokens)
}

func TestCreateImage_FillsMissingCreatedAndData(t *testing.T) {
	provider, _ := newTestProvider(t, jsonHandler(`{}`))

	resp, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"})
	require.NoError(t, err)
	assert.NotZero(t, resp.Created)
	assert.NotNil(t, resp.Data)
}

func TestCreateImage_RejectsInvalidRequests(t *testing.T) {
	provider, capture := newTestProvider(t, nil)

	tests := []struct {
		name    string
		req     *core.ImageGenerationRequest
		wantMsg string
	}{
		{"nil", nil, "image generation request is required"},
		{"empty prompt", &core.ImageGenerationRequest{Model: "dall-e-3"}, "prompt is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateImage(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
	assert.Zero(t, capture.Count(), "upstream must not be called for invalid requests")
}

func TestCreateImage_PropagatesUpstreamError(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected as a result of our safety system.","type":"invalid_request_error"}}`))
	})

	_, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safety system")
}
