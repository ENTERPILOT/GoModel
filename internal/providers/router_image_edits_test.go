package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockImageEditProvider supports edits as well as generation, mirroring the
// OpenAI-compatible provider.
type mockImageEditProvider struct {
	*mockImageProvider
	lastEditReq *core.ImageEditRequest
}

func (m *mockImageEditProvider) CreateImageEdit(_ context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	m.lastEditReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.imageResponse, nil
}

func editRequest(model, provider string) *core.ImageEditRequest {
	return &core.ImageEditRequest{
		Model:    model,
		Provider: provider,
		Prompt:   "add a hat",
		Images:   []core.ImageFile{{Filename: "cat.png", Data: []byte("png")}},
	}
}

func TestRouterCreateImageEdit(t *testing.T) {
	editor := &mockImageEditProvider{mockImageProvider: &mockImageProvider{
		mockProvider:  &mockProvider{name: "openai"},
		imageResponse: &core.ImageGenerationResponse{Created: 1, Data: []core.ImageData{{B64JSON: "aGk="}}},
	}}
	lookup := newMockLookup()
	lookup.addModel("openai/gpt-image-1", editor, "openai")
	router, _ := NewRouter(lookup)

	req := editRequest("gpt-image-1", "openai")
	resp, err := router.CreateImageEdit(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aGk=", resp.Data[0].B64JSON, "response = %+v", resp)
	assert.Equal(t, "openai", resp.Provider)
	require.NotNil(t, editor.lastEditReq)
	assert.Equal(t, "gpt-image-1", editor.lastEditReq.Model)
	assert.Empty(t, editor.lastEditReq.Provider)
	require.Len(t, editor.lastEditReq.Images, 1)
	assert.Equal(t, "png", string(editor.lastEditReq.Images[0].Data))
	assert.Equal(t, "openai", req.Provider)
}

func TestRouterCreateImageEdit_Errors(t *testing.T) {
	providerErr := errors.New("image edit provider failed")
	tests := []struct {
		name         string
		model        string
		provider     core.Provider
		providerType string
		req          *core.ImageEditRequest
		wantError    string
		wantIs       error
	}{
		{
			name:         "generation-only provider",
			model:        "xai/grok-imagine-image",
			provider:     &mockImageProvider{mockProvider: &mockProvider{name: "xai"}},
			providerType: "xai",
			req:          editRequest("grok-imagine-image", "xai"),
			wantError:    "does not support image edits",
		},
		{
			name:         "unsupported provider",
			model:        "anthropic/claude-sonnet-4",
			provider:     &mockProvider{name: "anthropic"},
			providerType: "anthropic",
			req:          editRequest("claude-sonnet-4", "anthropic"),
			wantError:    "does not support image edits",
		},
		{
			name:         "provider failure",
			model:        "openai/gpt-image-1",
			provider:     &mockImageEditProvider{mockImageProvider: &mockImageProvider{mockProvider: &mockProvider{name: "openai", err: providerErr}}},
			providerType: "openai",
			req:          editRequest("gpt-image-1", "openai"),
			wantIs:       providerErr,
		},
		{
			name:         "unknown model",
			model:        "openai/gpt-image-1",
			provider:     &mockImageEditProvider{mockImageProvider: &mockImageProvider{mockProvider: &mockProvider{name: "openai"}}},
			providerType: "openai",
			req:          editRequest("missing-model", ""),
			wantError:    "missing-model",
		},
		{
			name:      "nil request",
			wantError: "image edit request is required",
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

			_, err = router.CreateImageEdit(context.Background(), tt.req)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantError)
		})
	}
}
