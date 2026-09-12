package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockImageProvider struct {
	*mockProvider
	imageResponse *core.ImageGenerationResponse
	lastImageReq  *core.ImageGenerationRequest
}

func (m *mockImageProvider) CreateImage(_ context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	m.lastImageReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.imageResponse, nil
}

func TestRouterCreateImage(t *testing.T) {
	imager := &mockImageProvider{
		mockProvider:  &mockProvider{name: "openai"},
		imageResponse: &core.ImageGenerationResponse{Created: 1, Data: []core.ImageData{{URL: "https://img"}}},
	}
	lookup := newMockLookup()
	lookup.addModel("openai/dall-e-3", imager, "openai")
	router, _ := NewRouter(lookup)

	req := &core.ImageGenerationRequest{Model: "dall-e-3", Provider: "openai", Prompt: "a cat"}
	resp, err := router.CreateImage(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "https://img", resp.Data[0].URL, "response = %+v", resp)
	assert.Equal(t, "openai", resp.Provider)
	require.NotNil(t, imager.lastImageReq)
	assert.Equal(t, "dall-e-3", imager.lastImageReq.Model)
	assert.Empty(t, imager.lastImageReq.Provider)
	assert.Equal(t, "openai", req.Provider)
}

func TestRouterCreateImage_Errors(t *testing.T) {
	providerErr := errors.New("image provider failed")
	tests := []struct {
		name         string
		model        string
		provider     core.Provider
		providerType string
		req          *core.ImageGenerationRequest
		wantError    string
		wantIs       error
	}{
		{
			name:         "unsupported provider",
			model:        "anthropic/claude-sonnet-4",
			provider:     &mockProvider{name: "anthropic"},
			providerType: "anthropic",
			req:          &core.ImageGenerationRequest{Model: "claude-sonnet-4", Provider: "anthropic", Prompt: "a cat"},
			wantError:    "does not support image generation",
		},
		{
			name:         "provider failure",
			model:        "openai/dall-e-3",
			provider:     &mockImageProvider{mockProvider: &mockProvider{name: "openai", err: providerErr}},
			providerType: "openai",
			req:          &core.ImageGenerationRequest{Model: "dall-e-3", Provider: "openai", Prompt: "a cat"},
			wantIs:       providerErr,
		},
		{
			name:         "unknown model",
			model:        "openai/dall-e-3",
			provider:     &mockImageProvider{mockProvider: &mockProvider{name: "openai"}},
			providerType: "openai",
			req:          &core.ImageGenerationRequest{Model: "missing-model", Prompt: "a cat"},
			wantError:    "missing-model",
		},
		{
			name:      "nil request",
			wantError: "image generation request is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := newMockLookup()
			if tt.provider != nil {
				lookup.addModel(tt.model, tt.provider, tt.providerType)
			}
			router, err := NewRouter(lookup)
			require.NoError(t, err)

			_, err = router.CreateImage(context.Background(), tt.req)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantError)
		})
	}
}
