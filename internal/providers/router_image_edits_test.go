package providers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
	if err != nil {
		t.Fatalf("CreateImageEdit() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Provider != "openai" {
		t.Errorf("response provider = %q, want openai stamped", resp.Provider)
	}
	if editor.lastEditReq == nil {
		t.Fatal("image edit provider was not called")
	}
	if editor.lastEditReq.Model != "gpt-image-1" || editor.lastEditReq.Provider != "" {
		t.Errorf("forwarded selector = %q/%q, want provider metadata stripped", editor.lastEditReq.Provider, editor.lastEditReq.Model)
	}
	if len(editor.lastEditReq.Images) != 1 || string(editor.lastEditReq.Images[0].Data) != "png" {
		t.Errorf("forwarded images = %+v", editor.lastEditReq.Images)
	}
	if req.Provider != "openai" {
		t.Errorf("caller's request was mutated: provider = %q", req.Provider)
	}
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
			if err != nil {
				t.Fatalf("NewRouter() error = %v", err)
			}

			_, err = router.CreateImageEdit(context.Background(), tt.req)
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("CreateImageEdit() error = %v, want %v", err, tt.wantIs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("CreateImageEdit() error = %v, want message containing %q", err, tt.wantError)
			}
		})
	}
}
