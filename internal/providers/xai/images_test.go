package xai

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateImage verifies the xAI provider advertises image generation and
// delegates it to the OpenAI-compatible /images/generations endpoint: the
// request is POSTed with every field forwarded verbatim (xAI has no
// image-specific parameter mapping), upstream errors propagate, and sparse
// upstream responses are normalized to the OpenAI envelope.
func TestCreateImage(t *testing.T) {
	n := 2
	tests := []struct {
		name       string
		req        *core.ImageGenerationRequest
		statusCode int
		body       string
		wantErr    string
		wantBody   map[string]any // forwarded JSON fields that must be present
		check      func(*testing.T, *core.ImageGenerationResponse)
	}{
		{
			name:       "forwards required and optional fields",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat", N: &n, ResponseFormat: "url"},
			statusCode: http.StatusOK,
			body:       `{"created":1713833628,"data":[{"url":"https://imgen.x.ai/xai-imgen/img.jpg"},{"url":"https://imgen.x.ai/xai-imgen/img2.jpg"}]}`,
			wantBody:   map[string]any{"model": "grok-imagine-image", "prompt": "A cat", "n": float64(2), "response_format": "url"},
			check: func(t *testing.T, resp *core.ImageGenerationResponse) {
				assert.Equal(t, int64(1713833628), resp.Created)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "https://imgen.x.ai/xai-imgen/img.jpg", resp.Data[0].URL)
			},
		},
		{
			name:       "normalizes sparse response",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat"},
			statusCode: http.StatusOK,
			body:       `{}`,
			check: func(t *testing.T, resp *core.ImageGenerationResponse) {
				assert.NotEqual(t, int64(0), resp.Created)
				assert.NotNil(t, resp.Data)
			},
		},
		{
			name:       "propagates upstream error",
			req:        &core.ImageGenerationRequest{Model: "grok-imagine-image", Prompt: "A cat", Size: "1024x1024"},
			statusCode: http.StatusBadRequest,
			body:       `{"error":"Argument not supported: size"}`,
			wantErr:    "size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.body)
			provider := newTestProvider(server.URL)

			var imager core.ImageProvider = provider
			resp, err := imager.CreateImage(context.Background(), tt.req)

			req := capture.Last(t)
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t, "/images/generations", req.Path)
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
			sent := req.JSON(t)
			for key, want := range tt.wantBody {
				assert.Equal(t, want, sent[key], "forwarded field %q", key)
			}

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, resp)
		})
	}
}
